package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/custody"
)

type DepositHandler struct {
	service *custody.Service
	logger  *slog.Logger
}

func NewDepositHandler(service *custody.Service, logger *slog.Logger) *DepositHandler {
	return &DepositHandler{service: service, logger: logger}
}

func (h *DepositHandler) Address(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var request custody.DepositRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	address, err := h.service.DepositAddress(r.Context(), principal.UserID, request)
	if err != nil {
		switch {
		case errors.Is(err, custody.ErrInvalidDepositRequest):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_deposit_route"})
		case errors.Is(err, custody.ErrRouteUnavailable):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "deposit_route_unavailable"})
		case errors.Is(err, custody.ErrNetworkNotApproved):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "custody_network_not_approved"})
		case errors.Is(err, custody.ErrProviderUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "custody_unavailable"})
		default:
			h.logger.Error("deposit address request failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deposit_address_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, address)
}
