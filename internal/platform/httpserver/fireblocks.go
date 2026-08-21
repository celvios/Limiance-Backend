package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
)

func fireblocksWebhook(verifier *custody.WebhookVerifier, data *datamanager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if verifier == nil || data == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook_unavailable"})
			return
		}
		if r.Body == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_webhook"})
			return
		}
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(body) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_webhook"})
			return
		}
		if err := verifier.Verify(r.Context(), body, r.Header.Get("Fireblocks-Webhook-Signature")); err != nil {
			if errors.Is(err, custody.ErrMissingWebhookSignature) || errors.Is(err, custody.ErrInvalidWebhookSignature) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_webhook_signature"})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook_verification_unavailable"})
			return
		}
		accepted, err := data.RecordWebhookReceipt(r.Context(), "fireblocks", body)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook_persistence_unavailable"})
			return
		}
		if !accepted {
			writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}
