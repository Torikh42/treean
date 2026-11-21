package middleware

import (
	"net/http"
	"strings"
)

const (
	headerOrigin           = "Origin"
	headerVary             = "Vary"
	headerVaryReqMethod    = "Access-Control-Request-Method"
	headerVaryReqHeaders   = "Access-Control-Request-Headers"
	headerAllowOrigin      = "Access-Control-Allow-Origin"
	headerAllowMethods     = "Access-Control-Allow-Methods"
	headerAllowHeaders     = "Access-Control-Allow-Headers"
	headerExposeHeaders    = "Access-Control-Expose-Headers"
	headerMaxAge           = "Access-Control-Max-Age"
	corsAllowedMethods     = "GET, POST, PUT, DELETE, OPTIONS"
	corsAllowedHeaders     = "Content-Type, Authorization, X-User-ID, X-User-Role, Idempotency-Key"
	corsExposedHeaders     = "Idempotent-Replay, Retry-After"
	corsPreflightMaxAgeSec = "600"
)

// CORS applies origin allowlist headers and handles OPTIONS preflight requests without credentials.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll, exact := buildAllowedOriginMatcher(allowedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add(headerVary, headerOrigin)
			h.Add(headerVary, headerVaryReqMethod)
			h.Add(headerVary, headerVaryReqHeaders)

			if acao, ok := resolveAllowedOrigin(r.Header.Get(headerOrigin), allowAll, exact); ok {
				h.Set(headerAllowOrigin, acao)
				h.Set(headerAllowMethods, corsAllowedMethods)
				h.Set(headerAllowHeaders, corsAllowedHeaders)
				h.Set(headerExposeHeaders, corsExposedHeaders)
				h.Set(headerMaxAge, corsPreflightMaxAgeSec)
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if next != nil {
				next.ServeHTTP(w, r)
			}
		})
	}
}

func buildAllowedOriginMatcher(allowedOrigins []string) (bool, map[string]struct{}) {
	exact := make(map[string]struct{}, len(allowedOrigins))
	allowAll := false
	for _, origin := range allowedOrigins {
		trimmed := strings.TrimSpace(origin)
		if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
			continue
		}
		if trimmed == "*" {
			allowAll = true
			continue
		}
		exact[trimmed] = struct{}{}
	}
	return allowAll, exact
}

func resolveAllowedOrigin(rawOrigin string, allowAll bool, exact map[string]struct{}) (string, bool) {
	origin := strings.TrimSpace(rawOrigin)
	if origin == "" || strings.ContainsAny(origin, "\r\n") {
		return "", false
	}
	if allowAll {
		return "*", true
	}
	if _, ok := exact[origin]; ok {
		return origin, true
	}
	return "", false
}
