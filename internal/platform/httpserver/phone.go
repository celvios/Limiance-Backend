package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/phone"
)

type PhoneHandler struct {
	service *phone.Service
	logger  *slog.Logger
}
type phoneStartInput struct {
	Phone string `json:"phone"`
}
type phoneVerifyInput struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

func NewPhoneHandler(service *phone.Service, logger *slog.Logger) *PhoneHandler {
	return &PhoneHandler{service: service, logger: logger}
}

func (h *PhoneHandler) Start(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var input phoneStartInput
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.Start(r.Context(), p.UserID, input.Phone); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}

func (h *PhoneHandler) Verify(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var input phoneVerifyInput
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.Verify(r.Context(), p.UserID, input.Phone, input.Code); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "phone_verified"})
}

func (h *PhoneHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, phone.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_phone_verification"})
	case errors.Is(err, phone.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "phone_verification_unavailable"})
	case errors.Is(err, phone.ErrPhoneInUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "phone_in_use"})
	case errors.Is(err, phone.ErrRejected):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_or_expired_code"})
	default:
		h.logger.Error("phone verification failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "phone_verification_unavailable"})
	}
}
