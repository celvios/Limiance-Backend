package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/kyc"
)

type KYCHandler struct {
	sessionService *kyc.SessionService
	statusService  *kyc.Service
	logger         *slog.Logger
}

func NewKYCHandler(sessionService *kyc.SessionService, statusService *kyc.Service, logger *slog.Logger) *KYCHandler {
	return &KYCHandler{sessionService: sessionService, statusService: statusService, logger: logger}
}

func (h *KYCHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	session, err := h.sessionService.Create(r.Context(), principal.UserID)
	if err != nil {
		switch {
		case errors.Is(err, kyc.ErrNotConfigured):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "kyc_unavailable"})
		case errors.Is(err, kyc.ErrAlreadyApproved):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "kyc_already_approved"})
		case errors.Is(err, kyc.ErrAccountNotActive):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_not_active"})
		default:
			h.logger.Error("kyc session creation failed", "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kyc_session_failed"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"provider": "sumsub", "access_token": session.Token, "expires_in_seconds": session.ExpiresInSeconds})
}

func (h *KYCHandler) Status(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	profile, err := h.statusService.Status(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("kyc status read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "kyc_status_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, profile)
}
