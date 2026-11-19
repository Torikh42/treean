package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

func assertDomainError(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	de, ok := errors.AsType[*domain.DomainError](err)
	if !ok {
		t.Fatalf("expected *domain.DomainError, got %T (%v)", err, err)
	}
	if de.HTTPStatus != wantStatus || de.Code != wantCode {
		t.Fatalf("got (%d, %q), want (%d, %q)", de.HTTPStatus, de.Code, wantStatus, wantCode)
	}
}

func TestAggregateCheckoutItems_RejectsEmptyAndNilSlices(t *testing.T) {
	t.Parallel()

	for _, items := range [][]CheckoutItemInput{nil, {}} {
		_, _, err := aggregateCheckoutItems(items)
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity)
	}
}

func TestAggregateCheckoutItems_RejectsNilTicketTypeID(t *testing.T) {
	t.Parallel()

	idLow := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	cases := [][]CheckoutItemInput{
		{{TicketTypeID: uuid.Nil, Quantity: 1}},
		{{TicketTypeID: idLow, Quantity: 2}, {TicketTypeID: uuid.Nil, Quantity: 1}},
	}
	for _, items := range cases {
		_, _, err := aggregateCheckoutItems(items)
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	}
}

func TestAggregateCheckoutItems_RejectsInvalidQuantitiesAndOverflow(t *testing.T) {
	t.Parallel()

	idLow := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	idMid := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	idHigh := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	cases := [][]CheckoutItemInput{
		{{TicketTypeID: idLow, Quantity: 0}},
		{{TicketTypeID: idLow, Quantity: -1}},
		{{TicketTypeID: idLow, Quantity: math.MinInt32}},
		{{TicketTypeID: idLow, Quantity: 2}, {TicketTypeID: idLow, Quantity: 0}},
		{{TicketTypeID: idLow, Quantity: 2}, {TicketTypeID: idLow, Quantity: -1}},
		{{TicketTypeID: idLow, Quantity: 2}, {TicketTypeID: idLow, Quantity: 3}},
		{{TicketTypeID: idLow, Quantity: 2}, {TicketTypeID: idMid, Quantity: 2}, {TicketTypeID: idHigh, Quantity: 1}},
		{{TicketTypeID: idLow, Quantity: math.MaxInt32}},
		{{TicketTypeID: idLow, Quantity: math.MaxInt32}, {TicketTypeID: idLow, Quantity: 2}},
		{{TicketTypeID: idLow, Quantity: math.MaxInt32}, {TicketTypeID: idMid, Quantity: math.MaxInt32}},
	}
	for _, items := range cases {
		_, _, err := aggregateCheckoutItems(items)
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity)
	}
}

func TestAggregateCheckoutItems_BoundaryAndCanonicalOrder(t *testing.T) {
	t.Parallel()

	idLow := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	idMid := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	idHigh := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	cases := []struct {
		name      string
		in        []CheckoutItemInput
		wantTotal int32
		wantOut   []CheckoutItemInput
	}{
		{
			name:      "single tier exact max 4",
			in:        []CheckoutItemInput{{TicketTypeID: idLow, Quantity: 4}},
			wantTotal: 4,
			wantOut:   []CheckoutItemInput{{TicketTypeID: idLow, Quantity: 4}},
		},
		{
			name: "4 distinct tiers unsigned UUID byte sort",
			in: []CheckoutItemInput{
				{TicketTypeID: uuid.MustParse("a0000000-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("80000000-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("90000000-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("7fffffff-0000-0000-0000-000000000000"), Quantity: 1},
			},
			wantTotal: 4,
			wantOut: []CheckoutItemInput{
				{TicketTypeID: uuid.MustParse("7fffffff-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("80000000-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("90000000-0000-0000-0000-000000000000"), Quantity: 1},
				{TicketTypeID: uuid.MustParse("a0000000-0000-0000-0000-000000000000"), Quantity: 1},
			},
		},
		{
			name: "merges duplicate tiers and sorts ascending",
			in: []CheckoutItemInput{
				{TicketTypeID: idHigh, Quantity: 1},
				{TicketTypeID: idLow, Quantity: 1},
				{TicketTypeID: idMid, Quantity: 1},
				{TicketTypeID: idHigh, Quantity: 1},
			},
			wantTotal: 4,
			wantOut: []CheckoutItemInput{
				{TicketTypeID: idLow, Quantity: 1},
				{TicketTypeID: idMid, Quantity: 1},
				{TicketTypeID: idHigh, Quantity: 2},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, total, err := aggregateCheckoutItems(tc.in)
			if err != nil {
				t.Fatalf("unexpected error = %v", err)
			}
			if total != tc.wantTotal || len(out) != len(tc.wantOut) {
				t.Fatalf("got (len=%d, total=%d), want (len=%d, total=%d)", len(out), total, len(tc.wantOut), tc.wantTotal)
			}
			for i := range tc.wantOut {
				if out[i] != tc.wantOut[i] {
					t.Fatalf("out[%d] = %+v, want %+v", i, out[i], tc.wantOut[i])
				}
			}
		})
	}
}

func TestComputeCheckoutTotals_EmptyAndMissingTiers(t *testing.T) {
	t.Parallel()

	tierA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tierB := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	for _, items := range [][]CheckoutItemInput{nil, {}} {
		_, _, err := computeCheckoutTotals(items, map[uuid.UUID]lockedTier{
			tierA: {id: tierA, name: "Reguler", priceIDR: 150000, remaining: 10},
		})
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity)
	}

	missingCases := []struct {
		name  string
		items []CheckoutItemInput
		tiers map[uuid.UUID]lockedTier
	}{
		{
			name:  "single missing tier returns 404",
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 1}},
			tiers: map[uuid.UUID]lockedTier{tierB: {id: tierB, name: "VIP", priceIDR: 350000, remaining: 10}},
		},
		{
			name:  "missing tier wins 404 even when lower-UUID tier is sold out",
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 1}, {TicketTypeID: tierB, Quantity: 1}},
			tiers: map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Reguler", priceIDR: 150000, remaining: 0}},
		},
	}

	for _, tc := range missingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := computeCheckoutTotals(tc.items, tc.tiers)
			assertDomainError(t, err, http.StatusNotFound, domain.ErrCodeTicketTypeNotFound)
		})
	}
}

func TestComputeCheckoutTotals_QuotaAndAmountErrors(t *testing.T) {
	t.Parallel()

	tierA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tierB := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	_, _, err := computeCheckoutTotals(
		[]CheckoutItemInput{{TicketTypeID: tierA, Quantity: 2}, {TicketTypeID: tierB, Quantity: 2}},
		map[uuid.UUID]lockedTier{
			tierA: {id: tierA, name: "Reguler", priceIDR: 150000, remaining: 5},
			tierB: {id: tierB, name: "VIP", priceIDR: 350000, remaining: 1},
		},
	)
	assertDomainError(t, err, http.StatusConflict, domain.ErrCodeQuotaExhausted)

	validationCases := []struct {
		items []CheckoutItemInput
		tiers map[uuid.UUID]lockedTier
	}{
		{
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 1}},
			tiers: map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Reguler", priceIDR: -1, remaining: 5}},
		},
		{
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 0}},
			tiers: map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Reguler", priceIDR: 1000, remaining: 5}},
		},
		{
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: -2}},
			tiers: map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Reguler", priceIDR: 1000, remaining: 5}},
		},
	}
	for _, tc := range validationCases {
		_, _, err := computeCheckoutTotals(tc.items, tc.tiers)
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	}

	overflowCases := []struct {
		items []CheckoutItemInput
		tiers map[uuid.UUID]lockedTier
	}{
		{
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 2}},
			tiers: map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Whale", priceIDR: math.MaxInt64/2 + 1, remaining: 5}},
		},
		{
			items: []CheckoutItemInput{{TicketTypeID: tierA, Quantity: 1}, {TicketTypeID: tierB, Quantity: 1}},
			tiers: map[uuid.UUID]lockedTier{
				tierA: {id: tierA, name: "TierA", priceIDR: math.MaxInt64 - 10, remaining: 5},
				tierB: {id: tierB, name: "TierB", priceIDR: 11, remaining: 5},
			},
		},
	}
	for _, tc := range overflowCases {
		if _, _, err := computeCheckoutTotals(tc.items, tc.tiers); err == nil {
			t.Fatal("expected int64 overflow error")
		}
	}
}

func TestComputeCheckoutTotals_FreeAndMultiTierTotals(t *testing.T) {
	t.Parallel()

	tierA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tierB := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	freeItems, freeTotal, err := computeCheckoutTotals(
		[]CheckoutItemInput{{TicketTypeID: tierA, Quantity: 3}},
		map[uuid.UUID]lockedTier{tierA: {id: tierA, name: "Free Pass", priceIDR: 0, remaining: 3}},
	)
	if err != nil {
		t.Fatalf("free ticket unexpected error = %v", err)
	}
	if freeTotal != 0 || len(freeItems) != 1 || freeItems[0].SubtotalIDR != 0 {
		t.Fatalf("freeItems=%+v freeTotal=%d, want 0", freeItems, freeTotal)
	}

	orderItems, totalAmount, err := computeCheckoutTotals(
		[]CheckoutItemInput{{TicketTypeID: tierA, Quantity: 2}, {TicketTypeID: tierB, Quantity: 2}},
		map[uuid.UUID]lockedTier{
			tierA: {id: tierA, name: "Reguler", priceIDR: 150000, remaining: 2},
			tierB: {id: tierB, name: "VIP", priceIDR: 350000, remaining: 5},
		},
	)
	if err != nil {
		t.Fatalf("computeCheckoutTotals() unexpected error = %v", err)
	}
	if totalAmount != 1000000 || len(orderItems) != 2 {
		t.Fatalf("totalAmount=%d len=%d, want (1000000, 2)", totalAmount, len(orderItems))
	}
	if orderItems[0].SubtotalIDR != 300000 || orderItems[1].SubtotalIDR != 700000 {
		t.Fatalf("unexpected subtotals: %+v", orderItems)
	}
}

func TestFormatInvoiceNumber(t *testing.T) {
	t.Parallel()

	wib := time.FixedZone("WIB", 7*3600)
	est := time.FixedZone("EST", -5*3600)

	cases := []struct {
		name string
		now  time.Time
		seq  int64
		want string
	}{
		{
			name: "zero padded sequence 1 in 2026 UTC",
			now:  time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
			seq:  1,
			want: "INV-2026-000001",
		},
		{
			name: "wib early jan 1 converts to 2025 UTC year",
			now:  time.Date(2026, 1, 1, 2, 0, 0, 0, wib),
			seq:  42,
			want: "INV-2025-000042",
		},
		{
			name: "est late dec 31 converts to 2027 UTC year",
			now:  time.Date(2026, 12, 31, 21, 0, 0, 0, est),
			seq:  1234567,
			want: "INV-2027-1234567",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatInvoiceNumber(tc.now, tc.seq); got != tc.want {
				t.Fatalf("formatInvoiceNumber() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenerateTicketCode(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^TKT-261115-[0-9A-F]{10}$`)
	wib := time.FixedZone("WIB", 7*3600)
	// 2026-11-16 02:00 +07:00 equals 2026-11-15 19:00 UTC.
	now := time.Date(2026, 11, 16, 2, 0, 0, 0, wib)

	seen := make(map[string]struct{}, 200)
	for i := range 200 {
		code, err := generateTicketCode(now)
		if err != nil {
			t.Fatalf("generateTicketCode() iter %d error = %v", i, err)
		}
		if !pattern.MatchString(code) {
			t.Fatalf("generateTicketCode() = %q, want format matching %s", code, pattern.String())
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate ticket code generated in batch: %q", code)
		}
		seen[code] = struct{}{}
	}
}

func TestNormalizeWebhookPayload_EmptyAndValid(t *testing.T) {
	t.Parallel()

	for _, in := range [][]byte{nil, {}, []byte("   \t\n  ")} {
		if got := normalizeWebhookPayload(in); !bytes.Equal(got, []byte("{}")) {
			t.Fatalf("normalizeWebhookPayload(%q) = %q, want {}", in, got)
		}
	}

	validCases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{
			name: "valid json object",
			in:   []byte(`{"transaction_id":"tx-1","gross_amount":150000}`),
			want: []byte(`{"transaction_id":"tx-1","gross_amount":150000}`),
		},
		{
			name: "valid json array with outer whitespace",
			in:   []byte("  [{\"event\":\"capture\"}] \n"),
			want: []byte(`[{"event":"capture"}]`),
		},
		{
			name: "escaped literal backslash before u0000 is valid JSONB",
			in:   []byte(`{"path":"C:\\u0000"}`),
			want: []byte(`{"path":"C:\\u0000"}`),
		},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeWebhookPayload(tc.in)
			if !bytes.Equal(got, tc.want) || !json.Valid(got) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeWebhookPayload_InvalidAndEscaped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []byte
	}{
		{name: "truncated json", in: []byte(`{"order_id":"123"`)},
		{name: "plain text", in: []byte(`not json at all`)},
		{name: "raw nul byte in json", in: []byte("{\"note\":\"bad\x00value\"}")},
		{name: "lowercase unicode nul escape", in: []byte(`{"note":"bad\u0000value"}`)},
		{name: "mixed case unicode nul escape", in: []byte(`{"note":"bad\U0000value"}`)},
		{name: "odd backslashes real unicode nul escape", in: []byte(`{"note":"bad\\\u0000value"}`)},
		{name: "invalid utf8 bytes", in: []byte("{\"note\":\"\xff\xfe\"}")},
		{name: "oversized malformed ascii", in: []byte(strings.Repeat("x", 32*1024))},
		{name: "oversized json-escaped chars", in: []byte(strings.Repeat("<", 32*1024))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := normalizeWebhookPayload(tc.in)
			if !json.Valid(got) || !utf8.Valid(got) {
				t.Fatalf("normalized payload is not valid UTF-8 JSON: %q", got)
			}
			if bytes.Contains(got, []byte{0}) || containsUnicodeNulEscape(got) {
				t.Fatalf("normalized payload still contains NUL or unescaped unicode NUL: %q", got)
			}
			if len(got) > maxWebhookRawFallbackBytes {
				t.Fatalf("normalized payload size %d exceeds %d", len(got), maxWebhookRawFallbackBytes)
			}

			var env struct {
				RawInvalidJSON string `json:"raw_invalid_json"`
			}
			if err := json.Unmarshal(got, &env); err != nil {
				t.Fatalf("failed to unmarshal fallback envelope: %v", err)
			}
		})
	}
}

func TestNormalizeFailureReason(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "   \t\n ", "\x00\x00"} {
		if got := normalizeFailureReason(in); got != "payment failed by gateway" {
			t.Fatalf("normalizeFailureReason(%q) = %q, want default", in, got)
		}
	}

	if got := normalizeFailureReason("  gateway\x00 status: deny\xff  "); got != "gateway status: deny" {
		t.Fatalf("normalizeFailureReason() = %q, want %q", got, "gateway status: deny")
	}

	got := normalizeFailureReason(strings.Repeat("é", 300))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 255 {
		t.Fatalf("got valid=%v runes=%d, want valid UTF-8 with 255 runes", utf8.ValidString(got), utf8.RuneCountInString(got))
	}
}

func TestClassifyFailureTransition_RejectsInvalidStates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		order  *domain.Order
		nowUTC time.Time
	}{
		{name: "nil order", order: nil, nowUTC: now},
		{name: "zero now", order: &domain.Order{Status: domain.OrderStatusWaitingPayment, ExpiresAt: now.Add(time.Minute)}, nowUTC: time.Time{}},
		{name: "zero expires_at", order: &domain.Order{Status: domain.OrderStatusWaitingPayment}, nowUTC: now},
		{name: "paid order", order: &domain.Order{Status: domain.OrderStatusPaid, ExpiresAt: now.Add(time.Minute)}, nowUTC: now},
		{name: "cancelled order", order: &domain.Order{Status: domain.OrderStatusCancelled, ExpiresAt: now.Add(time.Minute)}, nowUTC: now},
		{name: "expired order", order: &domain.Order{Status: domain.OrderStatusExpired, ExpiresAt: now.Add(time.Minute)}, nowUTC: now},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := classifyFailureTransition(tc.order, "gateway status: deny", tc.nowUTC)
			assertDomainError(t, err, http.StatusConflict, domain.ErrCodeInvalidOrderState)
		})
	}
}

func TestClassifyFailureTransition_ValidTransitions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		expiresAt time.Time
		reason    string
		want      domain.OrderStatus
	}{
		{name: "overdue with deny reason expires", expiresAt: now.Add(-time.Second), reason: "gateway status: deny", want: domain.OrderStatusExpired},
		{name: "overdue with arbitrary reason expires", expiresAt: now.Add(-time.Second), reason: "card expired", want: domain.OrderStatusExpired},
		{name: "unexpired exact gateway expire status expires", expiresAt: now.Add(5 * time.Minute), reason: "gateway status: EXPIRE", want: domain.OrderStatusExpired},
		{name: "unexpired bare expire status expires", expiresAt: now.Add(5 * time.Minute), reason: "  expire  ", want: domain.OrderStatusExpired},
		{name: "unexpired card expired substring cancels", expiresAt: now.Add(5 * time.Minute), reason: "card expired", want: domain.OrderStatusCancelled},
		{name: "unexpired with deny cancels", expiresAt: now.Add(5 * time.Minute), reason: "gateway status: deny", want: domain.OrderStatusCancelled},
		{name: "exact deadline with cancel cancels", expiresAt: now, reason: "gateway status: cancel", want: domain.OrderStatusCancelled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			order := &domain.Order{ID: uuid.New(), Status: domain.OrderStatusWaitingPayment, ExpiresAt: tc.expiresAt}
			got, err := classifyFailureTransition(order, tc.reason, now)
			if err != nil {
				t.Fatalf("unexpected error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebhookRepo_LogDelivery_Guards(t *testing.T) {
	t.Parallel()

	repo := &WebhookRepo{}
	var nilRepo *WebhookRepo
	var nilCtx context.Context
	ctx := context.Background()

	cases := []struct {
		name string
		fn   func() error
	}{
		{name: "nil receiver", fn: func() error { return nilRepo.LogDelivery(ctx, &domain.PaymentWebhook{}) }},
		{name: "nil context", fn: func() error { return repo.LogDelivery(nilCtx, &domain.PaymentWebhook{}) }},
		{name: "nil webhook", fn: func() error { return repo.LogDelivery(ctx, nil) }},
		{name: "nil pool", fn: func() error { return repo.LogDelivery(ctx, &domain.PaymentWebhook{}) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.fn(); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestOrderRepo_NilReceiverGuards(t *testing.T) {
	t.Parallel()

	var nilRepo *OrderRepo
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	oid := uuid.New()
	uid := uuid.New()
	validItems := []CheckoutItemInput{{TicketTypeID: uuid.New(), Quantity: 1}}

	calls := []struct {
		name string
		fn   func() error
	}{
		{name: "CreateOrder", fn: func() error { _, err := nilRepo.CreateOrder(ctx, uid, uid, validItems, now); return err }},
		{name: "CancelOrder", fn: func() error { _, err := nilRepo.CancelOrder(ctx, oid, uid, now); return err }},
		{name: "ConfirmPayment", fn: func() error { _, err := nilRepo.ConfirmPayment(ctx, oid, 1000, now); return err }},
		{name: "FailPayment", fn: func() error { _, err := nilRepo.FailPayment(ctx, oid, "deny", now); return err }},
		{name: "ExpireDueOrders", fn: func() error { _, err := nilRepo.ExpireDueOrders(ctx, nil, 50, "scheduler", now); return err }},
		{name: "FindByID", fn: func() error { _, err := nilRepo.FindByID(ctx, oid); return err }},
		{name: "ListByBuyer", fn: func() error { _, err := nilRepo.ListByBuyer(ctx, uid); return err }},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.fn(); err == nil {
				t.Fatal("expected error for nil OrderRepo receiver")
			}
		})
	}
}

func TestOrderRepo_ZeroTimestampGuards(t *testing.T) {
	t.Parallel()

	repo := &OrderRepo{}
	ctx := context.Background()
	oid := uuid.New()
	uid := uuid.New()
	validItems := []CheckoutItemInput{{TicketTypeID: uuid.New(), Quantity: 1}}

	calls := []struct {
		name string
		fn   func() error
	}{
		{name: "CreateOrder", fn: func() error { _, err := repo.CreateOrder(ctx, uid, uid, validItems, time.Time{}); return err }},
		{name: "CancelOrder", fn: func() error { _, err := repo.CancelOrder(ctx, oid, uid, time.Time{}); return err }},
		{name: "ConfirmPayment", fn: func() error { _, err := repo.ConfirmPayment(ctx, oid, 1000, time.Time{}); return err }},
		{name: "FailPayment", fn: func() error { _, err := repo.FailPayment(ctx, oid, "deny", time.Time{}); return err }},
		{name: "ExpireDueOrders", fn: func() error {
			_, err := repo.ExpireDueOrders(ctx, nil, 50, "scheduler", time.Time{})
			return err
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.fn()
			if err == nil || strings.Contains(err.Error(), "database pool") {
				t.Fatalf("expected zero time error before pool check, got %v", err)
			}
		})
	}
}

func TestOrderRepo_ExpireDueOrdersGuards(t *testing.T) {
	t.Parallel()

	repo := &OrderRepo{}
	var nilCtx context.Context
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	if _, err := repo.ExpireDueOrders(nilCtx, nil, 50, "scheduler", now); err == nil {
		t.Fatal("expected error for nil context")
	}
	for _, actor := range []string{"", "   ", "admin", "cron", "buyer", "webhook", "SCHEDULER"} {
		if _, err := repo.ExpireDueOrders(ctx, nil, 50, actor, now); err == nil {
			t.Fatalf("expected error for invalid actorType %q", actor)
		}
	}
	nilEventID := uuid.Nil
	if _, err := repo.ExpireDueOrders(ctx, &nilEventID, 50, "lazy-check", now); err == nil {
		t.Fatal("expected error for pointer to uuid.Nil")
	}
	if _, err := repo.ExpireDueOrders(ctx, nil, 0, "scheduler", now); err == nil {
		t.Fatal("expected error for uninitialized pool")
	}
}

func TestOrderRepo_CreateOrderGuards(t *testing.T) {
	t.Parallel()

	repo := &OrderRepo{}
	var nilCtx context.Context
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	validItems := []CheckoutItemInput{{TicketTypeID: uuid.New(), Quantity: 1}}

	if _, err := repo.CreateOrder(nilCtx, uuid.New(), uuid.New(), validItems, now); err == nil {
		t.Fatal("expected error for nil context")
	}
	for _, pair := range [][2]uuid.UUID{{uuid.Nil, uuid.New()}, {uuid.New(), uuid.Nil}} {
		_, err := repo.CreateOrder(ctx, pair[0], pair[1], validItems, now)
		assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	}
	_, err := repo.CreateOrder(ctx, uuid.New(), uuid.New(), []CheckoutItemInput{{TicketTypeID: uuid.New(), Quantity: 5}}, now)
	assertDomainError(t, err, http.StatusUnprocessableEntity, domain.ErrCodeInvalidQuantity)

	if _, err := repo.CreateOrder(ctx, uuid.New(), uuid.New(), validItems, now); err == nil {
		t.Fatal("expected error for uninitialized pool")
	}
}

func TestOrderRepo_NilUUIDAndLookupGuards(t *testing.T) {
	t.Parallel()

	repo := &OrderRepo{}
	var nilCtx context.Context
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	oid := uuid.New()
	uid := uuid.New()

	nilOrderIDCalls := []func() error{
		func() error { _, err := repo.CancelOrder(ctx, uuid.Nil, uid, now); return err },
		func() error { _, err := repo.ConfirmPayment(ctx, uuid.Nil, 1000, now); return err },
		func() error { _, err := repo.FailPayment(ctx, uuid.Nil, "deny", now); return err },
		func() error { _, err := repo.FindByID(ctx, uuid.Nil); return err },
	}
	for _, fn := range nilOrderIDCalls {
		assertDomainError(t, fn(), http.StatusNotFound, domain.ErrCodeOrderNotFound)
	}

	nilBuyerIDCalls := []func() error{
		func() error { _, err := repo.CancelOrder(ctx, oid, uuid.Nil, now); return err },
		func() error { _, err := repo.ListByBuyer(ctx, uuid.Nil); return err },
	}
	for _, fn := range nilBuyerIDCalls {
		assertDomainError(t, fn(), http.StatusUnprocessableEntity, domain.ErrCodeValidation)
	}

	dbGuardCalls := []func() error{
		func() error { _, err := repo.CancelOrder(nilCtx, oid, uid, now); return err },
		func() error { _, err := repo.CancelOrder(ctx, oid, uid, now); return err },
		func() error { _, err := repo.ConfirmPayment(nilCtx, oid, 1000, now); return err },
		func() error { _, err := repo.ConfirmPayment(ctx, oid, 1000, now); return err },
		func() error { _, err := repo.FailPayment(nilCtx, oid, "deny", now); return err },
		func() error { _, err := repo.FailPayment(ctx, oid, "deny", now); return err },
		func() error { _, err := repo.FindByID(nilCtx, oid); return err },
		func() error { _, err := repo.FindByID(ctx, oid); return err },
		func() error { _, err := repo.ListByBuyer(nilCtx, uid); return err },
		func() error { _, err := repo.ListByBuyer(ctx, uid); return err },
	}
	for i, fn := range dbGuardCalls {
		if err := fn(); err == nil {
			t.Fatalf("guard call %d expected error, got nil", i)
		}
	}
}
