package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/conversions"
	"github.com/limiance/backend/internal/datamanager"
)

type ConversionHandler struct {
	service *conversions.Service
	logger  *slog.Logger
}

func NewConversionHandler(service *conversions.Service, logger *slog.Logger) *ConversionHandler {
	return &ConversionHandler{service: service, logger: logger}
}

func (h *ConversionHandler) Quote(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input conversions.QuoteInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	q, err := h.service.Quote(r.Context(), p.UserID, input)
	if err != nil {
		if errors.Is(err, conversions.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_conversion_quote"})
		} else if errors.Is(err, datamanager.ErrConversionsDisabled) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversions_temporarily_disabled"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_quote_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, q)
}

func (h *ConversionHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input conversions.ConfirmInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if input.QuoteID == "" {
		input.QuoteID = r.PathValue("quote_id")
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.service.Confirm(r.Context(), p.UserID, input)
	if err != nil {
		switch {
		case errors.Is(err, conversions.ErrInvalidInput):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_conversion_confirmation"})
		case errors.Is(err, datamanager.ErrConversionQuoteExpired):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conversion_quote_expired"})
		case errors.Is(err, datamanager.ErrConversionQuoteNotConfirmable):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conversion_quote_not_confirmable"})
		case errors.Is(err, datamanager.ErrConversionInsufficientBalance):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conversion_insufficient_balance"})
		case errors.Is(err, datamanager.ErrConversionsDisabled):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversions_temporarily_disabled"})
		default:
			h.logger.Error("conversion confirmation failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_unavailable"})
		}
		return
	}
	status := http.StatusCreated
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *ConversionHandler) History(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.History(r.Context(), p.UserID, historyLimit(r))
	if err != nil {
		h.logger.Error("conversion history read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_history_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversions": items})
}
