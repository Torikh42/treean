package domain_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

func TestOrder_CanPay(t *testing.T) {
	t.Parallel()

	baseNow := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		order      *domain.Order
		now        time.Time
		wantCode   string
		wantStatus int
	}{
		{
			name:  "waiting payment before deadline can pay",
			order: &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(10 * time.Minute)},
			now:   baseNow,
		},
		{
			name:  "waiting payment at exact expires_at boundary can pay",
			order: &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow},
			now:   baseNow,
		},
		{
			name:  "already paid returns nil for idempotent replay handling",
			order: &domain.Order{ID: uuid.New(), Status: domain.OrderStatusPaid, ExpiresAt: baseNow.Add(-5 * time.Minute)},
			now:   baseNow,
		},
		{
			name:       "waiting payment with zero now returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(10 * time.Minute)},
			now:        time.Time{},
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "waiting payment with zero expires_at returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: time.Time{}},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "waiting payment past expires_at returns ORDER_EXPIRED",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(-1 * time.Second)},
			now:        baseNow,
			wantCode:   domain.ErrCodeOrderExpired,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "already expired returns ORDER_EXPIRED",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusExpired, ExpiresAt: baseNow.Add(-20 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeOrderExpired,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "cancelled order cannot be paid",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusCancelled, ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "unknown status returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatus("UNKNOWN"), ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "nil order returns INVALID_ORDER_STATE",
			order:      nil,
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDomainCode(t, tt.order.CanPay(tt.now), tt.wantCode, tt.wantStatus)
		})
	}
}

func TestOrder_CanCancel(t *testing.T) {
	t.Parallel()

	baseNow := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		order      *domain.Order
		now        time.Time
		wantCode   string
		wantStatus int
	}{
		{
			name:  "waiting payment before expiry can cancel",
			order: &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:   baseNow,
		},
		{
			name:  "waiting payment at exact expires_at boundary can cancel",
			order: &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow},
			now:   baseNow,
		},
		{
			name:       "waiting payment with zero now returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        time.Time{},
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "waiting payment with zero expires_at returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: time.Time{}},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "waiting payment past expiry returns ORDER_EXPIRED",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: baseNow.Add(-1 * time.Second)},
			now:        baseNow,
			wantCode:   domain.ErrCodeOrderExpired,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "paid order cannot be cancelled",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusPaid, ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "already cancelled order returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusCancelled, ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "already expired order returns ORDER_EXPIRED",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatusExpired, ExpiresAt: baseNow.Add(-5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeOrderExpired,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "unknown status returns INVALID_ORDER_STATE",
			order:      &domain.Order{ID: uuid.New(), Status: domain.OrderStatus("PENDING"), ExpiresAt: baseNow.Add(5 * time.Minute)},
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "nil order returns INVALID_ORDER_STATE",
			order:      nil,
			now:        baseNow,
			wantCode:   domain.ErrCodeInvalidOrderState,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDomainCode(t, tt.order.CanCancel(tt.now), tt.wantCode, tt.wantStatus)
		})
	}
}

func TestTicketType_Validate(t *testing.T) {
	t.Parallel()

	maxSafePrice := math.MaxInt64 / int64(domain.MaxTicketsPerBuyerPerEvent)

	tests := []struct {
		name       string
		tier       domain.TicketType
		wantCode   string
		wantStatus int
	}{
		{
			name:       "empty name fails validation",
			tier:       domain.TicketType{Name: "", PriceIDR: 150000, TotalQuota: 50, RemainingQuota: 50},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "whitespace name fails validation",
			tier:       domain.TicketType{Name: "   \t  ", PriceIDR: 150000, TotalQuota: 50, RemainingQuota: 50},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "negative price fails validation",
			tier:       domain.TicketType{Name: "Reguler", PriceIDR: -1, TotalQuota: 50, RemainingQuota: 50},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "price above max safe 4-ticket total fails validation",
			tier:       domain.TicketType{Name: "Luxury", PriceIDR: maxSafePrice + 1, TotalQuota: 10, RemainingQuota: 10},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "zero quota fails validation",
			tier:       domain.TicketType{Name: "Reguler", PriceIDR: 150000, TotalQuota: 0},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "negative quota fails validation",
			tier:       domain.TicketType{Name: "Reguler", PriceIDR: 150000, TotalQuota: -10},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "negative remaining quota fails validation",
			tier:       domain.TicketType{Name: "Reguler", PriceIDR: 150000, TotalQuota: 50, RemainingQuota: -1},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "remaining quota exceeding total quota fails validation",
			tier:       domain.TicketType{Name: "Reguler", PriceIDR: 150000, TotalQuota: 50, RemainingQuota: 51},
			wantCode:   domain.ErrCodeValidation,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "zero remaining quota with positive total quota succeeds",
			tier: domain.TicketType{Name: "Sold Out Tier", PriceIDR: 150000, TotalQuota: 10, RemainingQuota: 0},
		},
		{
			name: "valid max safe boundary price succeeds",
			tier: domain.TicketType{Name: "Boundary VIP", PriceIDR: maxSafePrice, TotalQuota: 10, RemainingQuota: 10},
		},
		{
			name: "valid zero-IDR free ticket succeeds",
			tier: domain.TicketType{Name: "Community Free", PriceIDR: 0, TotalQuota: 100, RemainingQuota: 100},
		},
		{
			name: "valid paid ticket succeeds",
			tier: domain.TicketType{Name: "VIP", PriceIDR: 350000, TotalQuota: 20, RemainingQuota: 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDomainCode(t, tt.tier.Validate(), tt.wantCode, tt.wantStatus)
		})
	}
}

func TestVerifySHA512Signature(t *testing.T) {
	t.Parallel()

	orderID := "550e8400-e29b-41d4-a716-446655440000"
	secret := "test-webhook-secret"
	sig := domain.ComputeSHA512Signature(orderID, "200", 300000, secret)
	zeroSig := domain.ComputeSHA512Signature(orderID, "200", 0, secret)
	negSig := domain.ComputeSHA512Signature(orderID, "200", -500, secret)

	rawSum := sha512.Sum512([]byte(orderID + "200" + "300000" + secret))
	wantHex := hex.EncodeToString(rawSum[:])
	if sig != wantHex {
		t.Fatalf("ComputeSHA512Signature = %q, want %q", sig, wantHex)
	}

	tests := []struct {
		name        string
		orderID     string
		statusCode  string
		grossAmount int64
		secret      string
		providedSig string
		want        bool
	}{
		{name: "exact lowercase match", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: sig, want: true},
		{name: "uppercase hex normalized", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: strings.ToUpper(sig), want: true},
		{name: "trimmed whitespace normalized", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: "  " + sig + "  ", want: true},
		{name: "zero gross amount verifies", orderID: orderID, statusCode: "200", grossAmount: 0, secret: secret, providedSig: zeroSig, want: true},
		{name: "negative gross amount verifies", orderID: orderID, statusCode: "200", grossAmount: -500, secret: secret, providedSig: negSig, want: true},
		{name: "truncated signature fails", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: sig[:64], want: false},
		{name: "128-char non-hex signature fails", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: strings.Repeat("z", 128), want: false},
		{name: "mismatched gross amount", orderID: orderID, statusCode: "200", grossAmount: 300001, secret: secret, providedSig: sig, want: false},
		{name: "mismatched order id", orderID: uuid.NewString(), statusCode: "200", grossAmount: 300000, secret: secret, providedSig: sig, want: false},
		{name: "mismatched status code", orderID: orderID, statusCode: "201", grossAmount: 300000, secret: secret, providedSig: sig, want: false},
		{name: "wrong secret", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: "wrong-secret", providedSig: sig, want: false},
		{name: "empty secret fails closed", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: "", providedSig: sig, want: false},
		{name: "empty provided signature", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: "", want: false},
		{name: "whitespace provided signature", orderID: orderID, statusCode: "200", grossAmount: 300000, secret: secret, providedSig: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := domain.VerifySHA512Signature(tt.orderID, tt.statusCode, tt.grossAmount, tt.secret, tt.providedSig)
			if got != tt.want {
				t.Fatalf("VerifySHA512Signature() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainError(t *testing.T) {
	t.Parallel()

	var nilErr *domain.DomainError
	if nilErr.Error() != "" {
		t.Fatalf("nil DomainError.Error() = %q, want empty string", nilErr.Error())
	}

	codeOnlyErr := &domain.DomainError{Code: domain.ErrCodeValidation}
	if codeOnlyErr.Error() != domain.ErrCodeValidation {
		t.Fatalf("code-only Error() = %q, want %q", codeOnlyErr.Error(), domain.ErrCodeValidation)
	}

	emptyErr := &domain.DomainError{}
	if emptyErr.Error() != "domain error" {
		t.Fatalf("empty Error() = %q, want %q", emptyErr.Error(), "domain error")
	}

	cases := []struct {
		name       string
		err        *domain.DomainError
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{name: "generic", err: domain.NewDomainError(http.StatusNotFound, domain.ErrCodeEventNotFound, "missing event"), wantStatus: http.StatusNotFound, wantCode: domain.ErrCodeEventNotFound, wantMsg: "missing event"},
		{name: "quota exhausted", err: domain.NewQuotaExhaustedError("sold out"), wantStatus: http.StatusConflict, wantCode: domain.ErrCodeQuotaExhausted, wantMsg: "sold out"},
		{name: "buyer limit", err: domain.NewBuyerLimitExceededError("max 4"), wantStatus: http.StatusConflict, wantCode: domain.ErrCodeBuyerLimitExceeded, wantMsg: "max 4"},
		{name: "order expired", err: domain.NewOrderExpiredError("expired"), wantStatus: http.StatusConflict, wantCode: domain.ErrCodeOrderExpired, wantMsg: "expired"},
		{name: "invalid state", err: domain.NewInvalidOrderStateError("bad state"), wantStatus: http.StatusConflict, wantCode: domain.ErrCodeInvalidOrderState, wantMsg: "bad state"},
		{name: "amount mismatch", err: domain.NewPaymentAmountMismatchError("mismatch"), wantStatus: http.StatusBadRequest, wantCode: domain.ErrCodePaymentAmountMismatch, wantMsg: "mismatch"},
		{name: "unauthorized webhook", err: domain.NewUnauthorizedWebhookError("bad sig"), wantStatus: http.StatusUnauthorized, wantCode: domain.ErrCodeUnauthorizedWebhook, wantMsg: "bad sig"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.err.HTTPStatus != tc.wantStatus || tc.err.Code != tc.wantCode || tc.err.Error() != tc.wantMsg {
				t.Fatalf("got (%d, %q, %q), want (%d, %q, %q)", tc.err.HTTPStatus, tc.err.Code, tc.err.Error(), tc.wantStatus, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func TestUser_SerializationAndLogRedaction(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)
	u := domain.User{
		ID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Email:        "buyer@event.local",
		Name:         "Demo Buyer",
		PasswordHash: "$2a$10$supersecrethashvalue",
		Role:         domain.RoleBuyer,
		CreatedAt:    createdAt,
	}

	rawJSON, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("json.Marshal(User) failed: %v", err)
	}
	if strings.Contains(string(rawJSON), u.PasswordHash) {
		t.Fatalf("JSON output leaked password hash: %s", string(rawJSON))
	}
	var jsonFields map[string]any
	if err := json.Unmarshal(rawJSON, &jsonFields); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if _, exists := jsonFields["password_hash"]; exists {
		t.Fatalf("JSON output must omit password_hash key, got %s", string(rawJSON))
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("user loaded", slog.Any("user", u))
	rawLog := buf.String()
	if strings.Contains(rawLog, u.PasswordHash) {
		t.Fatalf("slog output leaked password hash: %s", rawLog)
	}

	var logPayload struct {
		User struct {
			ID           string    `json:"id"`
			Email        string    `json:"email"`
			Name         string    `json:"name"`
			PasswordHash string    `json:"password_hash"`
			Role         string    `json:"role"`
			CreatedAt    time.Time `json:"created_at"`
		} `json:"user"`
	}
	if err := json.Unmarshal(buf.Bytes(), &logPayload); err != nil {
		t.Fatalf("failed to unmarshal log output: %v (raw=%s)", err, rawLog)
	}
	if logPayload.User.PasswordHash != "[REDACTED]" {
		t.Fatalf("logged password_hash = %q, want [REDACTED]", logPayload.User.PasswordHash)
	}
	if logPayload.User.ID != u.ID.String() || logPayload.User.Email != u.Email ||
		logPayload.User.Name != u.Name || logPayload.User.Role != string(u.Role) ||
		!logPayload.User.CreatedAt.Equal(u.CreatedAt) {
		t.Fatalf("unexpected non-secret fields in log: %+v", logPayload.User)
	}
}

func assertDomainCode(t *testing.T, err error, wantCode string, wantStatus int) {
	t.Helper()

	if wantCode == "" {
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		return
	}

	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok {
		t.Fatalf("expected *DomainError, got %T (%v)", err, err)
	}
	if de.Code != wantCode {
		t.Fatalf("Code = %q, want %q", de.Code, wantCode)
	}
	if de.HTTPStatus != wantStatus {
		t.Fatalf("HTTPStatus = %d, want %d", de.HTTPStatus, wantStatus)
	}
}
