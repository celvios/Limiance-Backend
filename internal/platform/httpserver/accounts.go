package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

// WalletBalances is the exchange-facing account contract. The existing
// /v1/accounts/balances endpoint remains for compatibility; this endpoint
// makes the Funding/UTA separation explicit for new clients.
func (h *AccountHandler) WalletBalances(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	balances, err := h.service.Balances(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("wallet balances read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "balances_unavailable"})
		return
	}
	accountSummaries, err := h.service.Accounts(r.Context(), principal.UserID)
	if err != nil {
		h.logger.Error("account list read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "accounts_unavailable"})
		return
	}
	funding := make([]accounts.Balance, 0)
	uta := make([]accounts.Balance, 0)
	subaccounts := make([]accounts.Balance, 0)
	for _, balance := range balances {
		switch balance.AccountKind {
		case "funding":
			funding = append(funding, balance)
		case "uta":
			uta = append(uta, balance)
		case "subaccount":
			subaccounts = append(subaccounts, balance)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"funding":     funding,
		"uta":         uta,
		"subaccounts": subaccounts,
		"accounts":    accountSummaries,
	})
}

func (h *AccountHandler) Transactions(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	accountKind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account_kind")))
	if accountKind != "" && accountKind != "funding" && accountKind != "uta" && accountKind != "subaccount" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_account_kind"})
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
			return
		}
		limit = parsed
	}
	var cursor int64
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_cursor"})
			return
		}
		cursor = parsed
	}
	items, err := h.service.TransactionHistory(r.Context(), principal.UserID, accountKind, limit, cursor)
	if err != nil {
		h.logger.Error("account transaction history read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transaction_history_unavailable"})
		return
	}
	response := map[string]any{"transactions": items}
	if len(items) == limit {
		response["next_cursor"] = strconv.FormatInt(items[len(items)-1].PostingID, 10)
	}
	writeJSON(w, http.StatusOK, response)
}
