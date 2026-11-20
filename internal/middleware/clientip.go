package middleware

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const clientIPContextKey ctxKey = "client_ip"

// WithClientIP extracts the verified client IP into the request context.
// Forwarding headers (X-Real-IP / X-Forwarded-For) are trusted only when the direct TCP peer is loopback or private.
func WithClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractClientIP(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPContextKey, ip)))
	})
}

// ClientIPFromContext returns the client IP captured by WithClientIP.
func ClientIPFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ip, _ := ctx.Value(clientIPContextKey).(string)
	return ip
}

func extractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peerAddr, hasPeer := parseAddr(r.RemoteAddr)
	if hasPeer && isTrustedProxy(peerAddr) {
		if realIP, ok := parseAddr(r.Header.Get("X-Real-IP")); ok {
			return realIP.String()
		}
		if fwdIP, ok := rightmostValidForwardedFor(r.Header.Get("X-Forwarded-For")); ok {
			return fwdIP.String()
		}
	}
	if hasPeer {
		return peerAddr.String()
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func parseAddr(raw string) (netip.Addr, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	addr, err := netip.ParseAddr(strings.Trim(trimmed, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func rightmostValidForwardedFor(headerVal string) (netip.Addr, bool) {
	if strings.TrimSpace(headerVal) == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(headerVal, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if addr, ok := parseAddr(parts[i]); ok {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func isTrustedProxy(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate()
}
