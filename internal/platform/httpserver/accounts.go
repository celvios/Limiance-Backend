package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/accounts"
)

type AccountHandler struct {
	service *accounts.Service
	logger  *slog.Logger
}

func NewAccountHandler(service *accounts.Service, logger *slog.Logger) *AccountHandler {
	return &AccountHandler{service: service, logger: logger}
}

func (h *AccountHandler) Register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input accounts.RegisterInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	user, err := h.service.Register(r.Context(), input)
	if err != nil {
		h.logger.Error("registration failed", "error", err)
		switch {
		case errors.Is(err, accounts.ErrInvalidInput):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_registration"})
		case errors.Is(err, accounts.ErrEmailInUse):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "email_in_use"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registration_failed"})
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"user_id": user.ID, "uid": user.UID, "status": user.Status, "funding_account_id": user.Funding, "uta_account_id": user.UTA})
}

func (h *AccountHandler) Balances(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	balances, err := h.service.Balances(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("account balances read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "balances_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": balances})
}
