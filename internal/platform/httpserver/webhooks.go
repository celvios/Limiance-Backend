package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/limiance/backend/internal/kyc"
)

func sumsubWebhook(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
			return
		}
		if err := kyc.VerifyWebhook(secret, r.Header.Get("X-Payload-Digest-Alg"), r.Header.Get("X-Payload-Digest"), raw); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_signature"})
			return
		}
		digest := sha256.Sum256(raw)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "receipt": hex.EncodeToString(digest[:])})
	}
}
