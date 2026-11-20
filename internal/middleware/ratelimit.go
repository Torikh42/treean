package middleware

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const (
	defaultRateLimitInterval  = time.Minute
	registerRateLimitInterval = 15 * time.Minute
	unknownIPFallback         = "unknown"
	headerRetryAfter          = "Retry-After"
)

// RateLimiter checks whether a caller key may proceed within a fixed window.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

// RateLimitPolicy holds per-route fixed-window thresholds.
type RateLimitPolicy struct {
	Enabled          bool
	RegisterMax      int
	LoginIPMax       int
	OrderUserMax     int
	OrderIPMax       int
	DemoFlashSaleMax int
}

// NewHumaRateLimitMiddleware enforces fail-open Redis rate limits per route after authentication populates ActorFromContext.
func NewHumaRateLimitMiddleware(
	api huma.API,
	limiter RateLimiter,
	policy RateLimitPolicy,
) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if ctx == nil || next == nil {
			return
		}
		if !policy.Enabled || limiter == nil {
			next(ctx)
			return
		}

		op := ctx.Operation()
		if op == nil {
			next(ctx)
			return
		}

		clientIP := resolveRateLimitIP(ctx)
		if allowed, retryAfter := checkOperationLimits(ctx.Context(), op.OperationID, clientIP, limiter, policy); !allowed {
			writeRateLimitErr(api, ctx, retryAfter)
			return
		}
		next(ctx)
	}
}

func checkOperationLimits(
	ctx context.Context,
	operationID string,
	clientIP string,
	limiter RateLimiter,
	policy RateLimitPolicy,
) (bool, time.Duration) {
	switch operationID {
	case "register-user":
		return checkBucket(ctx, limiter, "rl:register:ip:"+clientIP, policy.RegisterMax, registerRateLimitInterval)
	case "login-user":
		return checkBucket(ctx, limiter, "rl:login:ip:"+clientIP, policy.LoginIPMax, defaultRateLimitInterval)
	case "create-order", "cancel-order":
		if actor, ok := ActorFromContext(ctx); ok && actor.ID != uuid.Nil {
			if okUser, waitUser := checkBucket(ctx, limiter, "rl:orders:user:"+actor.ID.String(), policy.OrderUserMax, defaultRateLimitInterval); !okUser {
				return false, waitUser
			}
		}
		return checkBucket(ctx, limiter, "rl:orders:ip:"+clientIP, policy.OrderIPMax, defaultRateLimitInterval)
	case "demo-flash-sale":
		return checkBucket(ctx, limiter, "rl:demo:ip:"+clientIP, policy.DemoFlashSaleMax, defaultRateLimitInterval)
	default:
		return true, 0
	}
}

func checkBucket(ctx context.Context, limiter RateLimiter, key string, limit int, window time.Duration) (bool, time.Duration) {
	if limit <= 0 || window <= 0 {
		return true, 0
	}
	allowed, retryAfter, err := limiter.Allow(ctx, key, limit, window)
	if err != nil {
		// Strict fail-open on limiter errors so transient store issues never reject ticket buyers.
		return true, 0
	}
	return allowed, retryAfter
}

func resolveRateLimitIP(ctx huma.Context) string {
	if ip := ClientIPFromContext(ctx.Context()); ip != "" {
		return ip
	}
	if addr, ok := parseAddr(ctx.RemoteAddr()); ok {
		return addr.String()
	}
	return unknownIPFallback
}

func writeRateLimitErr(api huma.API, ctx huma.Context, retryAfter time.Duration) {
	seconds := max(int(math.Ceil(retryAfter.Seconds())), 1)
	ctx.SetHeader(headerRetryAfter, strconv.Itoa(seconds))
	msg := fmt.Sprintf("rate limit exceeded; retry after %d seconds", seconds)
	writeAuthErr(api, ctx, http.StatusTooManyRequests, msg)
}
