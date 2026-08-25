package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/kyc"
)

func sumsubWebhook(secret string, data *datamanager.Manager, statusService *kyc.Service) http.HandlerFunc {
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
		created, err := data.RecordWebhookReceipt(r.Context(), "sumsub", raw)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook_unavailable"})
			return
		}
		if created {
			var event struct {
				ApplicantID  string `json:"applicantId"`
				Type         string `json:"type"`
				ReviewStatus string `json:"reviewStatus"`
				ReviewResult struct {
					Answer string `json:"reviewAnswer"`
				} `json:"reviewResult"`
			}
			if err := json.Unmarshal(raw, &event); err == nil && event.ApplicantID != "" {
				user, lookupErr := data.KYCUserByApplicant(r.Context(), event.ApplicantID)
				if lookupErr == nil {
					from := kyc.Status(user.KYCStatus)
					to := kyc.Pending
					if event.ReviewResult.Answer == "GREEN" {
						to = kyc.Approved
					}
					if event.ReviewResult.Answer == "RED" {
						to = kyc.Rejected
					}
					if event.ReviewStatus == "onHold" {
						to = kyc.OnHold
					}
					if event.Type == "applicantPending" {
						to = kyc.Pending
					}
					if from != to {
						_ = statusService.Transition(r.Context(), user.ID, from, to, tierForStatus(to), "sumsub", event.ApplicantID)
					}
				}
			}
		}
		digest := sha256.Sum256(raw)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "receipt": hex.EncodeToString(digest[:])})
	}
}

func tierForStatus(status kyc.Status) int16 {
	if status == kyc.Approved {
		return 1
	}
	return 0
}
