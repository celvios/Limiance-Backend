package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/marketmaker"
)

// MarketMakerAdminHandler exposes only audited state transitions. The worker's
// database tables are deliberately not writable through generic admin routes.
type MarketMakerAdminHandler struct {
	service *marketmaker.ActivationService
	logger  *slog.Logger
}

func NewMarketMakerAdminHandler(service *marketmaker.ActivationService, logger *slog.Logger) *MarketMakerAdminHandler {
	return &MarketMakerAdminHandler{service: service, logger: logger}
}

func (h *MarketMakerAdminHandler) Propose(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	var input marketmaker.ActivationInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid activation request")
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.service.Propose(r.Context(), p.UserID, input)
	if h.writeError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *MarketMakerAdminHandler) Approve(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		return
	}
	result, err := h.service.Approve(r.Context(), p.UserID, r.PathValue("request_id"))
	if h.writeError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *MarketMakerAdminHandler) EmergencyStop(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		Reason string `json:"reason"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_REQUEST", "reason is required")
		return
	}
	if h.writeError(w, h.service.EmergencyStop(r.Context(), p.UserID, input.Reason)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kill_switch": true})
}

func (h *MarketMakerAdminHandler) writeError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, marketmaker.ErrActivationInput):
		writeVersionedError(w, http.StatusBadRequest, "INVALID_ACTIVATION", "activation request is invalid")
	case errors.Is(err, marketmaker.ErrActivationRole):
		writeVersionedError(w, http.StatusForbidden, "TREASURY_ROLE_REQUIRED", "required treasury role is missing")
	case errors.Is(err, marketmaker.ErrSameChecker):
		writeVersionedError(w, http.StatusConflict, "DISTINCT_APPROVER_REQUIRED", "proposer cannot approve this request")
	case errors.Is(err, marketmaker.ErrActivationState):
		writeVersionedError(w, http.StatusConflict, "ACTIVATION_PRECONDITION_FAILED", "activation precondition failed")
	case errors.Is(err, marketmaker.ErrIdempotencyConflict):
		writeVersionedError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "idempotency key was used for another request")
	default:
		h.logger.Error("market maker activation failed", "error", err)
		writeVersionedError(w, http.StatusServiceUnavailable, "ACTIVATION_UNAVAILABLE", "market maker activation is unavailable")
	}
	return true
}
