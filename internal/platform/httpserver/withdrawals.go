package httpserver

import (
	"encoding/json"
	"errors"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/withdrawals"
	"log/slog"
	"net/http"
	"time"
)

type WithdrawalHandler struct {
	service                 *withdrawals.Service
	logger                  *slog.Logger
	cooldown                time.Duration
	travelRuleEncryptionKey string
}

func NewWithdrawalHandler(service *withdrawals.Service, logger *slog.Logger, cooldown time.Duration, travelRuleEncryptionKeys ...string) *WithdrawalHandler {
	key := ""
	if len(travelRuleEncryptionKeys) > 0 {
		key = travelRuleEncryptionKeys[0]
	}
	return &WithdrawalHandler{service: service, logger: logger, cooldown: cooldown, travelRuleEncryptionKey: key}
}

func (h *WithdrawalHandler) History(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.History(r.Context(), p.UserID, historyLimit(r))
	if err != nil {
		h.logger.Error("withdrawal history read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_history_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"withdrawals": items})
}

func (h *WithdrawalHandler) ListAddresses(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.ListAddresses(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("withdrawal address list failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_addresses_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": items})
}

func (h *WithdrawalHandler) AddAddress(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input withdrawals.AddressInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	item, err := h.service.AddAddress(r.Context(), p.UserID, input, h.cooldown)
	if err != nil {
		if errors.Is(err, withdrawals.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_withdrawal_address"})
		} else {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_address_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *WithdrawalHandler) DisableAddress(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	changed, err := h.service.DisableAddress(r.Context(), p.UserID, r.PathValue("address_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_withdrawal_address"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "withdrawal_address_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

func (h *WithdrawalHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	result, err := h.service.Cancel(r.Context(), p.UserID, r.PathValue("withdrawal_id"))
	if err != nil {
		if errors.Is(err, withdrawals.ErrInvalidInput) || errors.Is(err, datamanager.ErrWithdrawalNotCancellable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_not_cancellable"})
			return
		}
		h.logger.Error("withdrawal cancellation failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_cancellation_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *WithdrawalHandler) SaveTravelRule(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input withdrawals.TravelRuleInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	err := h.service.SaveTravelRule(r.Context(), p.UserID, r.PathValue("withdrawal_id"), h.travelRuleEncryptionKey, input)
	if errors.Is(err, withdrawals.ErrInvalidInput) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_travel_rule"})
		return
	}
	if errors.Is(err, withdrawals.ErrTravelRuleUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "travel_rule_unavailable"})
		return
	}
	if errors.Is(err, datamanager.ErrWithdrawalNotCancellable) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_not_travel_rule_eligible"})
		return
	}
	if err != nil {
		h.logger.Error("travel rule capture failed", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "travel_rule_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "captured"})
}

func (h *WithdrawalHandler) Request(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input withdrawals.Input
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.service.Request(r.Context(), p.UserID, input)
	if err != nil {
		if errors.Is(err, withdrawals.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_withdrawal"})
		} else if errors.Is(err, datamanager.ErrWithdrawalsDisabled) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawals_temporarily_disabled"})
		} else if errors.Is(err, datamanager.ErrWithdrawalAddressUnavailable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_address_unavailable", "message": "The withdrawal address is missing, inactive, or does not match this coin and network."})
		} else if errors.Is(err, datamanager.ErrWithdrawalKYCRequired) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_kyc_required", "message": "Complete identity verification before withdrawing."})
		} else if errors.Is(err, datamanager.ErrWithdrawalAccountUnavailable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_account_unavailable", "message": "The selected funding account is unavailable."})
		} else if errors.Is(err, datamanager.ErrWithdrawalIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency_conflict", "message": "This idempotency key was already used for a different withdrawal."})
		} else if errors.Is(err, datamanager.ErrWithdrawalDepositLimit) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_deposit_limit", "message": "Withdrawals are limited to your remaining confirmed deposits of this token on this network."})
		} else if errors.Is(err, datamanager.ErrInsufficientWithdrawalBalance) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient_withdrawal_balance", "message": "Your available balance is too low for this withdrawal."})
		} else if errors.Is(err, datamanager.ErrWithdrawalNotAllowed) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_route_unavailable", "message": "This asset and network withdrawal route is not enabled for staging."})
		} else {
			h.logger.Error("withdrawal request failed", "error", err)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_rejected"})
		}
		return
	}
	status := http.StatusCreated
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
