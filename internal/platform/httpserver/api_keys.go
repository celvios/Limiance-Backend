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
	"fmt"
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

type apiKeyRepository interface {
	APIKeyAuthentication(context.Context, []byte) (datamanager.APIKeyAuthentication, error)
	MarkAPIKeyUsed(context.Context, []byte) error
	ConsumeAPIKeyNonce(context.Context, []byte, []byte, time.Time) (bool, error)
}

type apiKeyAuthenticator struct {
	data          apiKeyRepository
	encryptionKey string
	now           func() time.Time
}

func requireSessionOrAPIKey(service *auth.Service, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	return requireSessionOrAPIKeyWithSession(requireSession(service), data, encryptionKey)
}

func requireAccountSessionOrAPIKey(service *auth.Service, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	return requireSessionOrAPIKeyWithSession(requireAccountSession(service), data, encryptionKey)
}

func requireSessionOrAPIKeyWithSession(sessionAuthentication func(http.Handler) http.Handler, data *datamanager.Manager, encryptionKey string) func(http.Handler) http.Handler {
	authenticator := apiKeyAuthenticator{data: data, encryptionKey: encryptionKey, now: time.Now}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") == "" {
				sessionAuthentication(next).ServeHTTP(w, r)
				return
			}
			authenticator.authenticate(next).ServeHTTP(w, r)
		})
	}
}

func (authenticator apiKeyAuthenticator) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("X-API-Key"))
		timestamp, err := strconv.ParseInt(r.Header.Get("X-API-Timestamp"), 10, 64)
		v2 := strings.HasPrefix(r.URL.Path, "/v2/")
		window := 5 * time.Minute
		if v2 {
			window = 30 * time.Second
		}
		if err != nil || authenticator.now().Sub(time.Unix(timestamp, 0)) > window || time.Unix(timestamp, 0).Sub(authenticator.now()) > window {
			authenticationError(w, r, http.StatusUnauthorized, "INVALID_API_SIGNATURE", "API signature timestamp is invalid")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			authenticationError(w, r, http.StatusBadRequest, "INVALID_BODY", "request body is invalid")
			return
		}
		_ = r.Body.Close()
		hash := sha256.Sum256([]byte(key))
		stored, err := authenticator.data.APIKeyAuthentication(r.Context(), hash[:])
		if err != nil {
			authenticationError(w, r, http.StatusUnauthorized, "INVALID_API_KEY", "API key is invalid")
			return
		}
		if len(stored.IPWhitelist) > 0 {
			if !apiIPAllowed(stored.IPWhitelist, remoteIP(r)) {
				authenticationError(w, r, http.StatusForbidden, "API_IP_NOT_ALLOWED", "client IP is not allowed")
				return
			}
		}
		secret, err := envelope.Open(authenticator.encryptionKey, stored.SecretCiphertext)
		if err != nil {
			authenticationError(w, r, http.StatusUnauthorized, "INVALID_API_KEY", "API key is invalid")
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		if v2 {
			nonce := strings.TrimSpace(r.Header.Get("X-API-Nonce"))
			if !validAPINonce(nonce) {
				authenticationError(w, r, http.StatusUnauthorized, "INVALID_API_NONCE", "API nonce is invalid")
				return
			}
			_, _ = mac.Write(v2SignaturePayload(r.Header.Get("X-API-Timestamp"), nonce, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body))
		} else {
			_, _ = mac.Write([]byte(r.Header.Get("X-API-Timestamp") + r.Method + r.URL.EscapedPath()))
			_, _ = mac.Write(body)
		}
		expected := mac.Sum(nil)
		provided, err := hex.DecodeString(r.Header.Get("X-API-Signature"))
		if err != nil || !hmac.Equal(expected, provided) {
			authenticationError(w, r, http.StatusUnauthorized, "INVALID_API_SIGNATURE", "API signature is invalid")
			return
		}
		if v2 {
			nonceHash := sha256.Sum256([]byte(strings.TrimSpace(r.Header.Get("X-API-Nonce"))))
			consumed, consumeErr := authenticator.data.ConsumeAPIKeyNonce(r.Context(), hash[:], nonceHash[:], authenticator.now().Add(2*time.Minute))
			if consumeErr != nil {
				authenticationError(w, r, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE", "API authentication is temporarily unavailable")
				return
			}
			if !consumed {
				authenticationError(w, r, http.StatusConflict, "API_NONCE_REPLAY", "API nonce has already been used")
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := authenticator.data.MarkAPIKeyUsed(r.Context(), hash[:]); err != nil {
			authenticationError(w, r, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE", "API authentication is temporarily unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, auth.Principal{SessionID: "", UserID: stored.UserID, UID: stored.UID, Email: stored.Email, ActiveAccountID: stored.AccountID, ActiveAccountKind: stored.AccountKind, APIKeyScope: stored.Scope, PrincipalType: "api_key"})))
	})
}

func v2SignaturePayload(timestamp, nonce, method, escapedPath, rawQuery string, body []byte) []byte {
	bodyHash := sha256.Sum256(body)
	return []byte(timestamp + "\n" + nonce + "\n" + strings.ToUpper(method) + "\n" + escapedPath + "\n" + rawQuery + "\n" + hex.EncodeToString(bodyHash[:]))
}

func validAPINonce(nonce string) bool {
	if len(nonce) < 16 || len(nonce) > 128 {
		return false
	}
	for _, character := range nonce {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func authenticationError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		writeVersionedError(w, status, code, message)
		return
	}
	writeJSON(w, status, map[string]string{"error": strings.ToLower(code)})
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
	normalizedIPs, ipErr := normalizeAPIIPWhitelist(input.IPWhitelist)
	if len(input.Name) < 1 || len(input.Name) > 80 || (input.Scope != "read_only" && input.Scope != "trade" && input.Scope != "withdraw") || ipErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_api_key"})
		return
	}
	input.IPWhitelist = normalizedIPs
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

func normalizeAPIIPWhitelist(values []string) ([]string, error) {
	if len(values) > 20 {
		return nil, fmt.Errorf("too many IP allowlist entries")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		normalized := ""
		if address := net.ParseIP(value); address != nil {
			normalized = address.String()
		} else if _, network, err := net.ParseCIDR(value); err == nil {
			normalized = network.String()
		} else {
			return nil, fmt.Errorf("invalid IP allowlist entry")
		}
		if _, exists := seen[normalized]; !exists {
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	return result, nil
}

func apiIPAllowed(allowlist []string, client string) bool {
	address := net.ParseIP(client)
	if address == nil {
		return false
	}
	for _, entry := range allowlist {
		if exact := net.ParseIP(entry); exact != nil && exact.Equal(address) {
			return true
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(address) {
			return true
		}
	}
	return false
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
