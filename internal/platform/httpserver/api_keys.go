package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/security/envelope"
	"github.com/limiance/backend/internal/security/session"
)

type APIKeyHandler struct {
	data        *datamanager.Manager
	envelopeKey string
}

func requireSessionOrAPIKey(service *auth.Service, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	return requireSessionOrAPIKeyWithSession(requireSession(service), data, encryptionKey)
}

func requireAccountSessionOrAPIKey(service *auth.Service, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	return requireSessionOrAPIKeyWithSession(requireAccountSession(service), data, encryptionKey)
}

func requireSessionOrAPIKeyWithSession(sessionAuthentication func(http.Handler) http.Handler, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") == "" {
				sessionAuthentication(next).ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("X-API-Key")
			timestamp, err := strconv.ParseInt(r.Header.Get("X-API-Timestamp"), 10, 64)
			if err != nil || time.Since(time.Unix(timestamp, 0)) > 5*time.Minute || time.Until(time.Unix(timestamp, 0)) > 5*time.Minute {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_signature"})
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
				return
			}
			_ = r.Body.Close()
			hash := sha256.Sum256([]byte(key))
			stored, err := data.APIKeyAuthentication(r.Context(), hash[:])
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_key"})
				return
			}
			if len(stored.IPWhitelist) > 0 {
				clientIP, _, splitErr := net.SplitHostPort(r.RemoteAddr)
				if splitErr != nil {
					clientIP = r.RemoteAddr
				}
				allowed := false
				for _, address := range stored.IPWhitelist {
					if strings.TrimSpace(address) == clientIP {
						allowed = true
						break
					}
				}
				if !allowed {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "api_ip_not_allowed"})
					return
				}
			}
			secret, err := envelope.Open(encryptionKey, stored.SecretCiphertext)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_key"})
				return
			}
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write([]byte(r.Header.Get("X-API-Timestamp") + r.Method + r.URL.EscapedPath()))
			_, _ = mac.Write(body)
			expected := mac.Sum(nil)
			provided, err := hex.DecodeString(r.Header.Get("X-API-Signature"))
			if err != nil || !hmac.Equal(expected, provided) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_signature"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if err := data.MarkAPIKeyUsed(r.Context(), hash[:]); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_key_unavailable"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, auth.Principal{SessionID: "", UserID: stored.UserID, UID: stored.UID, Email: stored.Email, ActiveAccountID: stored.AccountID, ActiveAccountKind: stored.AccountKind, APIKeyScope: stored.Scope, PrincipalType: "api_key"})))
		})
	}
}

func NewAPIKeyHandler(data *datamanager.Manager, envelopeKey string) *APIKeyHandler {
	return &APIKeyHandler{data: data, envelopeKey: envelopeKey}
}

func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	keys, err := h.data.APIKeys(r.Context(), principal.UserID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_keys_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
}

func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if principal.ActiveAccountKind == "subaccount" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "api_key_main_account_required"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input struct {
		Name        string   `json:"name"`
		Scope       string   `json:"scope"`
		IPWhitelist []string `json:"ip_whitelist"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if len(input.Name) < 1 || len(input.Name) > 80 || (input.Scope != "read_only" && input.Scope != "trade" && input.Scope != "withdraw") || len(input.IPWhitelist) > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_api_key"})
		return
	}
	stepUp := strings.TrimSpace(r.Header.Get("X-Step-Up-Token"))
	if stepUp == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "step_up_required"})
		return
	}
	valid, err := h.data.ConsumeMFAStepUpChallenge(r.Context(), principal.UserID, principal.SessionID, "api_key_created", session.Hash(stepUp))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "step_up_unavailable"})
		return
	}
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_step_up_token"})
		return
	}

	publicBytes := make([]byte, 18)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(publicBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "api_key_unavailable"})
		return
	}
	if _, err := rand.Read(secretBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "api_key_unavailable"})
		return
	}
	publicKey := "lm_" + base64.RawURLEncoding.EncodeToString(publicBytes)
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	sealedSecret, err := envelope.Seal(h.envelopeKey, secret)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_key_encryption_unavailable"})
		return
	}
	hash := sha256.Sum256([]byte(publicKey))
	key, err := h.data.CreateAPIKey(r.Context(), principal.UserID, input.Name, input.Scope, hash[:], sealedSecret, input.IPWhitelist)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_key_creation_failed"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": key, "key": publicKey, "secret": secret})
}

func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	keyID := strings.TrimSpace(r.PathValue("key_id"))
	if keyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_api_key"})
		return
	}
	changed, err := h.data.RevokeAPIKey(r.Context(), principal.UserID, keyID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api_key_revoke_failed"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "api_key_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
