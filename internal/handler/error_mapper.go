// Package handler implements Huma v2 HTTP endpoints, OpenAPI schemas, and response mapping.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/middleware"
)

const (
	bearerScheme    = "bearer"
	errCodeInternal = "INTERNAL_ERROR"
	fieldOrder      = "order"
)

var (
	humaErrorFactoryOnce sync.Once
	bearerSecurity       = []map[string][]string{{bearerScheme: {}}}
)

// APIError is the uniform RFC-style error payload enriched with machine-readable error_code.
type APIError struct {
	status    int
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail"`
	ErrorCode string              `json:"error_code"`
	Errors    []*huma.ErrorDetail `json:"errors,omitempty"`
}

// Error implements the Go error interface.
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}

// GetStatus satisfies huma.StatusError so Huma renders the exact HTTP status code.
func (e *APIError) GetStatus() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	if e.status != 0 {
		return e.status
	}
	return e.Status
}

func configureHumaErrorFactory() {
	humaErrorFactoryOnce.Do(func() {
		huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
			return &APIError{
				status:    status,
				Title:     http.StatusText(status),
				Status:    status,
				Detail:    msg,
				ErrorCode: mapHTTPStatusToErrorCode(status),
				Errors:    toErrorDetails(errs),
			}
		}
	})
}

type errorDetailerErr interface {
	error
	huma.ErrorDetailer
}

func toErrorDetails(errs []error) []*huma.ErrorDetail {
	if len(errs) == 0 {
		return nil
	}
	details := make([]*huma.ErrorDetail, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		if detailer, ok := errors.AsType[errorDetailerErr](err); ok {
			if d := detailer.ErrorDetail(); d != nil {
				details = append(details, d)
				continue
			}
		}
		details = append(details, &huma.ErrorDetail{Message: err.Error()})
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func mapHTTPStatusToErrorCode(status int) string {
	switch status {
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return domain.ErrCodeValidation
	case http.StatusUnauthorized:
		return domain.ErrCodeUnauthorized
	case http.StatusForbidden:
		return domain.ErrCodeForbidden
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusInternalServerError:
		return errCodeInternal
	default:
		return "HTTP_ERROR"
	}
}

func safeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return logger
}

func mapServiceError(ctx context.Context, logger *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	if de, ok := errors.AsType[*domain.DomainError](err); ok {
		return &APIError{
			status:    de.HTTPStatus,
			Title:     http.StatusText(de.HTTPStatus),
			Status:    de.HTTPStatus,
			Detail:    de.Message,
			ErrorCode: de.Code,
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	safeLogger(logger).ErrorContext(ctx, "unhandled service error", slog.String("error", err.Error()))
	return &APIError{
		status:    http.StatusInternalServerError,
		Title:     http.StatusText(http.StatusInternalServerError),
		Status:    http.StatusInternalServerError,
		Detail:    "internal server error",
		ErrorCode: errCodeInternal,
	}
}

func requireActor(ctx context.Context) (domain.Actor, error) {
	actor, ok := middleware.ActorFromContext(ctx)
	if !ok || actor.ID == uuid.Nil {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "missing authenticated actor")
	}
	return actor, nil
}

func parseNonNilUUID(raw, fieldName string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, domain.NewDomainError(http.StatusBadRequest, domain.ErrCodeValidation, "invalid "+fieldName+" UUID")
	}
	return id, nil
}
