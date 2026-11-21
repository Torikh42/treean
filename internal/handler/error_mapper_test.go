package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/handler"
)

func TestAPIError_NilAndStatusBehavior(t *testing.T) {
	t.Parallel()

	var nilErr *handler.APIError
	if nilErr.Error() != "" {
		t.Fatalf("nil APIError.Error() = %q, want empty", nilErr.Error())
	}
	if nilErr.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("nil APIError.GetStatus() = %d, want 500", nilErr.GetStatus())
	}
	exportedOnly := &handler.APIError{Status: http.StatusBadRequest, Detail: "bad"}
	if exportedOnly.Error() != "bad" || exportedOnly.GetStatus() != http.StatusBadRequest {
		t.Fatalf("unexpected Error/GetStatus on exportedOnly: %q / %d", exportedOnly.Error(), exportedOnly.GetStatus())
	}

	handler.ConfigureHumaErrorFactory()
	plainErr := huma.NewError(http.StatusUnprocessableEntity, "validation failed", errors.New("plain field err"))
	raw, err := json.Marshal(plainErr)
	if err != nil {
		t.Fatalf("marshal huma error: %v", err)
	}
	if !strings.Contains(string(raw), `"plain field err"`) {
		t.Fatalf("expected serialized error detail message in %s", string(raw))
	}

	rateErr := domain.NewRateLimitedError("too many login attempts").WithRetryAfter(15 * time.Second)
	mapped := handler.MapServiceError(context.Background(), nil, rateErr)
	var headersErr huma.HeadersError
	if !errors.As(mapped, &headersErr) {
		t.Fatalf("expected mapped rate-limited error to implement huma.HeadersError, got %T", mapped)
	}
	if got := headersErr.GetHeaders().Get("Retry-After"); got != "15" {
		t.Fatalf("Retry-After header = %q, want 15", got)
	}
}
