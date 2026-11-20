package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/middleware"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
)

type stubHandlerAuth struct {
	actor domain.Actor
}

func (s *stubHandlerAuth) ParseJWT(string) (domain.Actor, error) { return s.actor, nil }
func (s *stubHandlerAuth) EnsureDemoUser(context.Context, uuid.UUID, domain.Role) error {
	return nil
}

type stubIdempOrderRepo struct {
	service.OrderRepository
	replay bool
	order  *domain.Order
}

func (r *stubIdempOrderRepo) CreateOrderIdempotent(
	_ context.Context,
	_, _ uuid.UUID,
	_ []repository.CheckoutItemInput,
	_ string,
	_ time.Time,
) (*repository.CreateOrderResult, error) {
	return &repository.CreateOrderResult{
		Order:            r.order,
		IdempotentReplay: r.replay,
	}, nil
}

func TestOrderHandler_CreateSetsExactIdempotentReplayHeader(t *testing.T) {
	t.Parallel()

	eventID := uuid.New()
	buyerID := uuid.New()
	tierID := uuid.New()
	repo := &stubIdempOrderRepo{
		replay: false,
		order: &domain.Order{
			ID:             uuid.New(),
			InvoiceNumber:  "INV-2026-000001",
			EventID:        eventID,
			BuyerID:        buyerID,
			TotalTickets:   1,
			TotalAmountIDR: 150000,
			Status:         domain.OrderStatusWaitingPayment,
			ExpiresAt:      time.Now().UTC().Add(15 * time.Minute),
			CreatedAt:      time.Now().UTC(),
		},
	}

	r := chi.NewRouter()
	cfg := huma.DefaultConfig("Order Header Test", "1.0.0")
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}
	api := humachi.New(r, cfg)
	api.UseMiddleware(middleware.NewHumaAuthMiddleware(
		api,
		&stubHandlerAuth{actor: domain.Actor{ID: buyerID, Role: domain.RoleBuyer}},
		slog.New(slog.DiscardHandler),
		false,
	))

	h := handler.NewOrderHandler(service.NewOrderService(repo, nil), nil)
	huma.Register(api, huma.Operation{
		OperationID:   "create-order",
		Method:        http.MethodPost,
		Path:          "/api/v1/orders",
		DefaultStatus: http.StatusCreated,
		Security:      []map[string][]string{{"bearer": {}}},
	}, h.Create)

	fire := func() *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"event_id": eventID.String(),
			"items":    []map[string]any{{"ticket_type_id": tierID.String(), "quantity": 1}},
		})
		if err != nil {
			t.Fatalf("marshal request payload: %v", err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer fake")
		req.Header.Set("Idempotency-Key", "k-123")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	first := fire()
	if first.Code != http.StatusCreated || first.Header().Get("Idempotent-Replay") != "" {
		t.Fatalf("first create = (%d, replay=%q), want 201 and empty header", first.Code, first.Header().Get("Idempotent-Replay"))
	}

	repo.replay = true
	second := fire()
	if second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay create = (%d, replay=%q), want 201 and Idempotent-Replay=true", second.Code, second.Header().Get("Idempotent-Replay"))
	}
}
