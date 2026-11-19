package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
)

const internalErrorBody = `{"title":"Internal Server Error","status":500,"detail":"internal server error","error_code":"INTERNAL_ERROR"}`

func safeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return logger
}

// Recoverer recovers from handler panics, logs structured stack traces once, and returns JSON 500 if uncommitted.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	log := safeLogger(logger)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww, ok := w.(chiMiddleware.WrapResponseWriter)
			if !ok {
				ww = chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			}
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.ErrorContext(r.Context(), "http handler panic recovered",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("request_id", chiMiddleware.GetReqID(r.Context())),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				if ww.Status() == 0 {
					ww.Header().Set(headerContentType, mimeApplicationJSON)
					ww.WriteHeader(http.StatusInternalServerError)
					_, _ = ww.Write([]byte(internalErrorBody))
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// RequestLogger logs HTTP method, path, status code, duration, and request ID without logging credentials.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	log := safeLogger(logger)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}
				log.InfoContext(r.Context(), "http request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", status),
					slog.Int64("duration_ms", time.Since(start).Milliseconds()),
					slog.String("request_id", chiMiddleware.GetReqID(r.Context())),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// MaxBodyBytes caps incoming request body size to mitigate memory exhaustion payloads.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && limit > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
