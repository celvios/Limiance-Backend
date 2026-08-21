package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/transfers"
)

type TransferHandler struct {
	service *transfers.Service
	logger  *slog.Logger
}

func NewTransferHandler(service *transfers.Service, logger *slog.Logger) *TransferHandler {
	return &TransferHandler{service: service, logger: logger}
}

func (h *TransferHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input transfers.Input
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.service.Create(r.Context(), principal.UserID, input)
	if err != nil {
		if errors.Is(err, transfers.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_transfer"})
			return
		}
		h.logger.Error("internal transfer failed", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "transfer_rejected"})
		return
	}
	status := http.StatusCreated
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *TransferHandler) ResolveRecipient(w http.ResponseWriter, r *http.Request) {
	if _, ok := principalFromContext(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input transfers.RecipientInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	recipient, err := h.service.ResolveRecipient(r.Context(), input)
	if err != nil {
		if errors.Is(err, transfers.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_recipient"})
		} else {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "recipient_not_found"})
		}
		return
	}
	writeJSON(w, http.StatusOK, recipient)
}
