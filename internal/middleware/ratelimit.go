package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
)

const (
	defaultRateLimitInterval  = time.Minute
	registerRateLimitInterval = 15 * time.Minute
	defaultPoWNonceTTL        = 120 * time.Second
	unknownIPFallback         = "unknown"
	headerRetryAfter          = "Retry-After"
	headerPoWChallenge        = "X-PoW-Challenge"
	headerPoWSolution         = "X-PoW-Solution"
)

// RateLimiter checks whether a caller key may proceed within a fixed window.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

// ChallengeGuard issues and verifies stateless Proof-of-Work challenges bound to client IP.
type ChallengeGuard interface {
	Issue(clientIP string, now time.Time) (string, error)
	Verify(solutionHeader, clientIP string, now time.Time) (string, error)
}

// NonceConsumer atomically consumes a single-use Proof-of-Work challenge key.
type NonceConsumer interface {
	ConsumeOnce(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// RateLimitPolicy holds per-route fixed-window thresholds and optional adaptive PoW verification.
type RateLimitPolicy struct {
	Enabled          bool
	RegisterMax      int
	RegisterHardMax  int
	LoginIPMax       int
	LoginIPHardMax   int
	OrderUserMax     int
	OrderIPMax       int
	DemoFlashSaleMax int
	Challenge        ChallengeGuard
	NonceStore       NonceConsumer
	PoWTTL           time.Duration
}

type rateLimitOutcome struct {
	allowed           bool
	challengeRequired bool
	retryAfter        time.Duration
	powChallenge      string
}

type challengeErrorBody struct {
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	ErrorCode string `json:"error_code"`
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
		outcome := checkOperationLimits(ctx.Context(), ctx, op.OperationID, clientIP, limiter, policy)
		if !outcome.allowed {
			if outcome.challengeRequired {
				writeChallengeErr(api, ctx, outcome.powChallenge)
				return
			}
			writeRateLimitErr(api, ctx, outcome.retryAfter)
			return
		}
		next(ctx)
	}
}

func checkOperationLimits(
	ctx context.Context,
	humaCtx huma.Context,
	operationID string,
	clientIP string,
	limiter RateLimiter,
	policy RateLimitPolicy,
) rateLimitOutcome {
	switch operationID {
	case "register-user":
		return checkAdaptiveLimit(
			ctx,
			humaCtx,
			limiter,
			"rl:register:ip:"+clientIP,
			"rl:register:ip:hard:"+clientIP,
			policy.RegisterMax,
			policy.RegisterHardMax,
			registerRateLimitInterval,
			clientIP,
			policy,
		)
	case "login-user":
		return checkAdaptiveLimit(
			ctx,
			humaCtx,
			limiter,
			"rl:login:ip:"+clientIP,
			"rl:login:ip:hard:"+clientIP,
			policy.LoginIPMax,
			policy.LoginIPHardMax,
			defaultRateLimitInterval,
			clientIP,
			policy,
		)
	case "create-order", "cancel-order":
		if actor, ok := ActorFromContext(ctx); ok && actor.ID != uuid.Nil {
			if okUser, waitUser := checkBucket(ctx, limiter, "rl:orders:user:"+actor.ID.String(), policy.OrderUserMax, defaultRateLimitInterval); !okUser {
				return rateLimitOutcome{retryAfter: waitUser}
			}
		}
		if policy.OrderIPMax > 0 {
			if okIP, waitIP := checkBucket(ctx, limiter, "rl:orders:ip:"+clientIP, policy.OrderIPMax, defaultRateLimitInterval); !okIP {
				return rateLimitOutcome{retryAfter: waitIP}
			}
		}
		return rateLimitOutcome{allowed: true}
	case "demo-flash-sale":
		okDemo, waitDemo := checkBucket(ctx, limiter, "rl:demo:ip:"+clientIP, policy.DemoFlashSaleMax, defaultRateLimitInterval)
		return rateLimitOutcome{allowed: okDemo, retryAfter: waitDemo}
	default:
		return rateLimitOutcome{allowed: true}
	}
}

func checkAdaptiveLimit(
	ctx context.Context,
	humaCtx huma.Context,
	limiter RateLimiter,
	softKey, hardKey string,
	softMax, hardMax int,
	window time.Duration,
	clientIP string,
	policy RateLimitPolicy,
) rateLimitOutcome {
	if hardMax > 0 {
		if okHard, waitHard := checkBucket(ctx, limiter, hardKey, hardMax, window); !okHard {
			return rateLimitOutcome{retryAfter: waitHard}
		}
	}
	if okSoft, waitSoft := checkBucket(ctx, limiter, softKey, softMax, window); okSoft {
		return rateLimitOutcome{allowed: true}
	} else if policy.Challenge == nil {
		return rateLimitOutcome{retryAfter: waitSoft}
	}

	now := time.Now().UTC()
	if verifyPoWSolution(ctx, humaCtx, clientIP, now, policy) {
		return rateLimitOutcome{allowed: true}
	}

	// Write X-PoW-Challenge only when issuance succeeds; fail closed on PoW if issuance fails.
	challenge, err := policy.Challenge.Issue(clientIP, now)
	if err != nil {
		return rateLimitOutcome{challengeRequired: true}
	}
	return rateLimitOutcome{
		challengeRequired: true,
		powChallenge:      challenge,
	}
}

func verifyPoWSolution(
	ctx context.Context,
	humaCtx huma.Context,
	clientIP string,
	now time.Time,
	policy RateLimitPolicy,
) bool {
	if humaCtx == nil || policy.Challenge == nil {
		return false
	}
	solution := strings.TrimSpace(humaCtx.Header(headerPoWSolution))
	if solution == "" {
		return false
	}
	nonceKey, err := policy.Challenge.Verify(solution, clientIP, now)
	if err != nil {
		return false
	}
	if policy.NonceStore == nil {
		return true
	}
	ttl := policy.PoWTTL
	if ttl <= 0 {
		ttl = defaultPoWNonceTTL
	}
	fresh, consumeErr := policy.NonceStore.ConsumeOnce(ctx, nonceKey, ttl)
	if consumeErr != nil {
		// Strict fail-open on replay-store outages so verified solvers are not blocked.
		return true
	}
	return fresh
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

func writeChallengeErr(api huma.API, ctx huma.Context, challenge string) {
	if challenge != "" {
		ctx.SetHeader(headerPoWChallenge, challenge)
	}
	body := challengeErrorBody{
		Title:     http.StatusText(http.StatusForbidden),
		Status:    http.StatusForbidden,
		Detail:    "proof-of-work verification required",
		ErrorCode: domain.ErrCodeChallengeRequired,
	}
	ctx.SetHeader(headerContentType, mimeApplicationJSON)
	ctx.SetStatus(http.StatusForbidden)
	if api != nil {
		_ = api.Marshal(ctx.BodyWriter(), mimeApplicationJSON, body)
		return
	}
	if payload, err := json.Marshal(body); err == nil {
		_, _ = ctx.BodyWriter().Write(payload)
	}
}

func writeRateLimitErr(api huma.API, ctx huma.Context, retryAfter time.Duration) {
	seconds := max(int(math.Ceil(retryAfter.Seconds())), 1)
	ctx.SetHeader(headerRetryAfter, strconv.Itoa(seconds))
	msg := fmt.Sprintf("rate limit exceeded; retry after %d seconds", seconds)
	writeAuthErr(api, ctx, http.StatusTooManyRequests, msg)
}
