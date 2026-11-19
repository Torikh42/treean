// Package middleware provides HTTP request logging, panic recovery, body limits, and hybrid authentication.
package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

type ctxKey string

const actorContextKey ctxKey = "auth_actor"

const (
	bearerPrefix        = "bearer "
	demoActorEmail      = "demo@event.local"
	headerContentType   = "Content-Type"
	mimeApplicationJSON = "application/json"
)

// Authenticator verifies bearer JWT tokens and provisions opt-in demo accounts.
type Authenticator interface {
	ParseJWT(tokenStr string) (domain.Actor, error)
	EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error
}

// ActorFromContext extracts the authenticated Actor stored by NewHumaAuthMiddleware.
func ActorFromContext(ctx context.Context) (domain.Actor, bool) {
	if ctx == nil {
		return domain.Actor{}, false
	}
	actor, ok := ctx.Value(actorContextKey).(domain.Actor)
	return actor, ok
}

// NewHumaAuthMiddleware validates Bearer JWT or opt-in demo headers on protected Huma operations.
func NewHumaAuthMiddleware(
	api huma.API,
	auth Authenticator,
	logger *slog.Logger,
	allowDemoHeaders bool,
) func(ctx huma.Context, next func(huma.Context)) {
	log := safeLogger(logger)
	return func(ctx huma.Context, next func(huma.Context)) {
		if ctx == nil || next == nil {
			return
		}
		op := ctx.Operation()
		if op == nil || len(op.Security) == 0 {
			next(ctx)
			return
		}
		if auth == nil {
			writeAuthErr(api, ctx, http.StatusUnauthorized, "unauthorized: auth verifier unavailable")
			return
		}

		if tokenStr, hasBearer := parseBearerToken(ctx.Header("Authorization")); hasBearer {
			if tokenStr == "" {
				writeAuthErr(api, ctx, http.StatusUnauthorized, "unauthorized: empty bearer token")
				return
			}
			actor, err := auth.ParseJWT(tokenStr)
			if err != nil {
				writeAuthErr(api, ctx, http.StatusUnauthorized, "unauthorized: invalid or expired bearer token")
				return
			}
			next(huma.WithValue(ctx, actorContextKey, actor))
			return
		}

		if allowDemoHeaders {
			actor, handled := authenticateDemoHeaders(api, ctx, auth, log)
			if handled {
				if actor.ID != uuid.Nil {
					next(huma.WithValue(ctx, actorContextKey, actor))
				}
				return
			}
		}

		writeAuthErr(api, ctx, http.StatusUnauthorized, "missing authentication credentials")
	}
}

func parseBearerToken(header string) (string, bool) {
	trimmed := strings.TrimSpace(header)
	if strings.EqualFold(trimmed, "bearer") {
		return "", true
	}
	if len(trimmed) > len(bearerPrefix) && strings.EqualFold(trimmed[:len(bearerPrefix)], bearerPrefix) {
		return strings.TrimSpace(trimmed[len(bearerPrefix):]), true
	}
	return "", false
}

func authenticateDemoHeaders(api huma.API, ctx huma.Context, auth Authenticator, log *slog.Logger) (domain.Actor, bool) {
	uidStr := strings.TrimSpace(ctx.Header("X-User-ID"))
	roleStr := strings.ToUpper(strings.TrimSpace(ctx.Header("X-User-Role")))
	if uidStr == "" && roleStr == "" {
		return domain.Actor{}, false
	}

	uid, err := uuid.Parse(uidStr)
	role := domain.Role(roleStr)
	if err != nil || uid == uuid.Nil || (role != domain.RoleBuyer && role != domain.RoleOrganizer) {
		writeAuthErr(api, ctx, http.StatusUnauthorized, "unauthorized: invalid X-User-ID or X-User-Role")
		return domain.Actor{}, true
	}

	if err := auth.EnsureDemoUser(ctx.Context(), uid, role); err != nil {
		log.ErrorContext(ctx.Context(), "demo user provisioning failed",
			slog.String("user_id", uid.String()),
			slog.String("role", string(role)),
			slog.String("error", err.Error()),
		)
		writeAuthErr(api, ctx, http.StatusInternalServerError, "failed to prepare demo user identity")
		return domain.Actor{}, true
	}
	return domain.Actor{ID: uid, Email: demoActorEmail, Role: role}, true
}

func writeAuthErr(api huma.API, ctx huma.Context, status int, msg string) {
	if api != nil {
		// Huma logs marshaling/write failures internally when writing error responses.
		_ = huma.WriteErr(api, ctx, status, msg)
		return
	}
	ctx.SetHeader(headerContentType, mimeApplicationJSON)
	ctx.SetStatus(status)
	payload, err := json.Marshal(huma.NewError(status, msg))
	if err == nil {
		_, _ = ctx.BodyWriter().Write(payload)
	}
}
