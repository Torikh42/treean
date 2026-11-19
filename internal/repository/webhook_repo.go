package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/domain"
)

const (
	// maxWebhookRawFallbackBytes bounds raw non-JSON audit payloads stored in PostgreSQL JSONB.
	maxWebhookRawFallbackBytes = 16 * 1024
	unicodeNulEscapeLen        = 6
)

type invalidWebhookEnvelope struct {
	RawInvalidJSON string `json:"raw_invalid_json"`
}

// WebhookRepo records payment callback audit entries outside business rollback transactions.
type WebhookRepo struct {
	pool *pgxpool.Pool
}

// NewWebhookRepo constructs a WebhookRepo.
func NewWebhookRepo(pool *pgxpool.Pool) *WebhookRepo {
	return &WebhookRepo{pool: pool}
}

// LogDelivery inserts an audit record directly on the pool so rollbacks never erase failed or late attempts.
func (r *WebhookRepo) LogDelivery(ctx context.Context, w *domain.PaymentWebhook) error {
	if ctx == nil {
		return errors.New("logging payment webhook: context is required")
	}
	if r == nil {
		return errors.New("logging payment webhook: repository is nil")
	}
	if w == nil {
		return errors.New("logging payment webhook: nil webhook")
	}
	if r.pool == nil {
		return errors.New("logging payment webhook: database pool is not initialized")
	}

	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	rawPayload := normalizeWebhookPayload(w.RawPayload)

	const query = `
		INSERT INTO payment_webhooks (
			id, transaction_id, raw_order_id, parsed_order_id, status_code,
			gross_amount, transaction_status, outcome, requires_refund, raw_payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING received_at`

	if err := r.pool.QueryRow(ctx, query,
		w.ID, w.TransactionID, w.RawOrderID, w.ParsedOrderID, w.StatusCode,
		w.GrossAmount, w.TransactionStatus, string(w.Outcome), w.RequiresRefund, rawPayload,
	).Scan(&w.ReceivedAt); err != nil {
		return fmt.Errorf("inserting payment webhook audit log: %w", err)
	}
	return nil
}

// normalizeWebhookPayload guarantees valid UTF-8, NUL-free JSON bytes for PostgreSQL JSONB NOT NULL.
func normalizeWebhookPayload(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}")
	}
	if isSafePostgresJSONB(trimmed) {
		return trimmed
	}
	return wrapInvalidWebhookPayload(trimmed)
}

func isSafePostgresJSONB(trimmed []byte) bool {
	if !utf8.Valid(trimmed) {
		return false
	}
	if bytes.IndexByte(trimmed, 0) >= 0 {
		return false
	}
	if containsUnicodeNulEscape(trimmed) {
		return false
	}
	return json.Valid(trimmed)
}

func wrapInvalidWebhookPayload(trimmed []byte) []byte {
	const envelopeOverhead = len(`{"raw_invalid_json":""}`)

	sample := trimmed
	if len(sample) > maxWebhookRawFallbackBytes*2 {
		sample = sample[:maxWebhookRawFallbackBytes*2]
	}

	clean := strings.ToValidUTF8(string(sample), "")
	clean = strings.ReplaceAll(clean, "\x00", "")
	for containsUnicodeNulEscapeString(clean) {
		clean = stripUnicodeNulEscapes(clean)
	}
	clean = truncateValidUTF8Bytes(clean, maxWebhookRawFallbackBytes-envelopeOverhead)

	for {
		encoded, err := json.Marshal(invalidWebhookEnvelope{RawInvalidJSON: clean})
		if err != nil {
			return []byte("{}")
		}
		if len(encoded) <= maxWebhookRawFallbackBytes || len(clean) == 0 {
			return encoded
		}
		clean = truncateValidUTF8Bytes(clean, len(clean)/2)
	}
}

func containsUnicodeNulEscape(b []byte) bool {
	return containsUnicodeNul(b)
}

func containsUnicodeNulEscapeString(s string) bool {
	return containsUnicodeNul(s)
}

func containsUnicodeNul[T ~[]byte | ~string](buf T) bool {
	if len(buf) < unicodeNulEscapeLen {
		return false
	}
	for i := range len(buf) - unicodeNulEscapeLen + 1 {
		if isUnicodeNulEscapeAt(buf, i) {
			return true
		}
	}
	return false
}

func isUnicodeNulEscapeAt[T ~[]byte | ~string](buf T, i int) bool {
	if i+unicodeNulEscapeLen > len(buf) {
		return false
	}
	if buf[i] != '\\' || (buf[i+1] != 'u' && buf[i+1] != 'U') ||
		buf[i+2] != '0' || buf[i+3] != '0' || buf[i+4] != '0' || buf[i+5] != '0' {
		return false
	}
	backslashes := 0
	for j := i - 1; j >= 0 && buf[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func stripUnicodeNulEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if isUnicodeNulEscapeAt(s, i) {
			i += unicodeNulEscapeLen
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func truncateValidUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := 0
	for end < len(s) {
		_, size := utf8.DecodeRuneInString(s[end:])
		if end+size > maxBytes {
			break
		}
		end += size
	}
	return s[:end]
}
