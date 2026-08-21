package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestID(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set("X-Request-ID", id)
			start := time.Now()
			next.ServeHTTP(w, r)
			logger.Info("request complete", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic recovered", "panic", recovered, "stack", string(debug.Stack()))
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b)
}
