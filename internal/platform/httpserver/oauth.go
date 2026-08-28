package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/security/session"
	"golang.org/x/oauth2"
)

type OAuthHandler struct {
	auth     *AuthHandler
	data     *datamanager.Manager
	provider *auth.OIDCProvider
}

func NewOAuthHandler(authHandler *AuthHandler, data *datamanager.Manager, provider *auth.OIDCProvider) *OAuthHandler {
	return &OAuthHandler{auth: authHandler, data: data, provider: provider}
}

func (h *OAuthHandler) Start(w http.ResponseWriter, r *http.Request) {
	state, stateHash, err := session.New()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
		return
	}
	verifier := oauth2.GenerateVerifier()
	nonce, _, err := session.New()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
		return
	}
	if err := h.data.CreateOAuthState(r.Context(), stateHash, h.provider.Name, h.provider.Redirect, verifier, nonce, time.Now().UTC().Add(10*time.Minute)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": h.provider.AuthURL(state, verifier, nonce)})
}

func (h *OAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_oauth_callback"})
		return
	}
	verifier, nonce, err := h.data.ConsumeOAuthState(r.Context(), session.Hash(state), h.provider.Name, h.provider.Redirect)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_or_expired_oauth_state"})
		return
	}
	claims, err := h.provider.Claims(r.Context(), code, nonce, verifier)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "external_authentication_failed"})
		return
	}
	result, err := h.auth.service.LoginExternal(r.Context(), h.provider.Name, claims, sessionMetadataFromRequest(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAccountFrozen):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_frozen"})
		case errors.Is(err, auth.ErrVerificationNeeded):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "email_verification_required"})
		default:
			h.auth.logger.Error("external login failed", "provider", h.provider.Name, "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
		}
		return
	}
	h.auth.setSessionCookie(w, result)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated"})
}

func sessionMetadataFromRequest(r *http.Request) datamanager.SessionMetadata {
	userAgent, clientIP := requestDeviceMetadata(r)
	return datamanager.SessionMetadata{UserAgent: userAgent, ClientIP: clientIP}
}
