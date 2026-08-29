package httpserver

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/datamanager"
)

var allowedCORSMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
}

var allowedCORSHeaders = map[string]struct{}{
	"content-type":             {},
	"idempotency-key":          {},
	"x-request-id":             {},
	"x-step-up-token":          {},
	"x-geetest-lot-number":     {},
	"x-geetest-captcha-output": {},
	"x-geetest-pass-token":     {},
	"x-geetest-gen-time":       {},
	"x-api-key":                {},
	"x-api-timestamp":          {},
	"x-api-nonce":              {},
	"x-api-signature":          {},
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// customerCORS provides credentialed browser access only to the deployed
// customer and operations frontends. Cookie-authenticated CORS must always
// reflect an explicit, trusted origin; a wildcard is unsafe and invalid with
// credentials.
func customerCORS(allowedOrigins []string, next http.Handler) http.Handler {
	origins := originSet(allowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !isAllowedBrowserOrigin(origins, origin) {
			if isPreflight(r) {
				http.Error(w, "cors origin denied", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
		if !isPreflight(r) {
			next.ServeHTTP(w, r)
			return
		}

		requestedMethod := r.Header.Get("Access-Control-Request-Method")
		if _, ok := allowedCORSMethods[requestedMethod]; !ok {
			http.Error(w, "cors method denied", http.StatusForbidden)
			return
		}
		if !allowedRequestedHeaders(r.Header.Get("Access-Control-Request-Headers")) {
			http.Error(w, "cors header denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Request-ID, X-Step-Up-Token, X-API-Key, X-API-Timestamp, X-API-Nonce, X-API-Signature, X-GeeTest-Lot-Number, X-GeeTest-Captcha-Output, X-GeeTest-Pass-Token, X-GeeTest-Gen-Time")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		w.WriteHeader(http.StatusNoContent)
	})
}

func requireStepUp(service *auth.Service, purpose string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromContext(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		valid, err := service.ConsumeStepUp(r.Context(), principal, purpose, r.Header.Get("X-Step-Up-Token"))
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "step_up_unavailable"})
			return
		}
		if !valid {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "step_up_required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireWithdrawalStepUp(service *auth.Service, data *datamanager.Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromContext(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var input struct {
			AssetSymbol string `json:"asset_symbol"`
			Network     string `json:"network"`
			Address     string `json:"address"`
			Tag         string `json:"tag"`
		}
		_ = json.Unmarshal(body, &input)
		trusted, err := data.IsWithdrawalAddressWhitelisted(r.Context(), principal.UserID, strings.ToUpper(strings.TrimSpace(input.AssetSymbol)), strings.ToLower(strings.TrimSpace(input.Network)), strings.TrimSpace(input.Address), strings.TrimSpace(input.Tag))
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_policy_unavailable"})
			return
		}
		if trusted {
			next.ServeHTTP(w, r)
			return
		}
		valid, err := service.ConsumeStepUp(r.Context(), principal, "withdrawal_created", r.Header.Get("X-Step-Up-Token"))
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "step_up_unavailable"})
			return
		}
		if !valid {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "step_up_required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfOriginCheck stops cross-site form submissions when cross-site session
// cookies are required by the deployed Vercel frontends. Non-browser clients
// may omit Origin and continue to use the API directly.
func csrfOriginCheck(allowedOrigins []string, next http.Handler) http.Handler {
	origins := originSet(allowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnsafeMethod(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !isAllowedBrowserOrigin(origins, origin) {
				if strings.HasPrefix(r.URL.Path, "/v2/") {
					writeVersionedError(w, http.StatusForbidden, "ORIGIN_DENIED", "request origin is not allowed")
				} else {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin_denied"})
				}
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func originSet(allowedOrigins []string) map[string]struct{} {
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[origin] = struct{}{}
	}
	return origins
}

func isAllowedBrowserOrigin(origins map[string]struct{}, origin string) bool {
	_, ok := origins[origin]
	return ok
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func allowedRequestedHeaders(headers string) bool {
	for _, header := range strings.Split(headers, ",") {
		header = strings.TrimSpace(strings.ToLower(header))
		if header == "" {
			continue
		}
		if _, ok := allowedCORSHeaders[header]; !ok {
			return false
		}
	}
	return true
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
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
					if strings.HasPrefix(r.URL.Path, "/v2/") {
						writeVersionedError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "an internal error occurred")
					} else {
						writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
					}
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
