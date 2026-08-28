package httpserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/limiance/backend/internal/auth"
)

type principalContextKey struct{}

func requireSession(service *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
				return
			}
			principal, err := service.Authenticate(r.Context(), cookie.Value)
			if err != nil {
				if errors.Is(err, auth.ErrAccountFrozen) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_frozen"})
					return
				}
				if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrVerificationNeeded) {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
					return
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
				return
			}
			if principal.PrincipalType == "subaccount" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "subaccount_scope_not_enabled"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
		})
	}
}

func requireAccountSession(service *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
				return
			}
			principal, err := service.Authenticate(r.Context(), cookie.Value)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
				return
			}
			if principal.ActiveAccountID == "" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_context_required"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
		})
	}
}

func principalFromContext(r *http.Request) (auth.Principal, bool) {
	principal, ok := r.Context().Value(principalContextKey{}).(auth.Principal)
	return principal, ok
}
