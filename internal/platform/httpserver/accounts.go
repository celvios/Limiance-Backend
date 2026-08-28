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

func (h *AccountHandler) SwitchAccount(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var input struct {
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&input); err != nil || strings.TrimSpace(input.AccountID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_account"})
		return
	}
	if err := h.service.SwitchAccount(r.Context(), principal.UserID, principal.SessionID, input.AccountID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_not_available"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"active_account_id": input.AccountID})
}

type createSubaccountInput struct {
	Nickname                string `json:"nickname"`
	Type                    string `json:"type"`
	AccountMode             string `json:"account_mode"`
	Username                string `json:"username"`
	Password                string `json:"password"`
	RequirePasswordForLogin bool   `json:"require_password_for_login"`
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

func (h *AccountHandler) Subaccounts(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if r.Method == http.MethodGet {
		items, err := h.service.Subaccounts(r.Context(), principal.UserID)
		if err != nil {
			h.logger.Error("subaccounts read failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "subaccounts_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"subaccounts": items})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input createSubaccountInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	item, err := h.service.CreateSubaccount(r.Context(), principal.UserID, accounts.SubaccountInput{
		Nickname: input.Nickname, Type: input.Type, AccountMode: input.AccountMode,
		Username: input.Username, Password: input.Password, RequirePasswordForLogin: input.RequirePasswordForLogin,
	})
	if err != nil {
		if errors.Is(err, accounts.ErrInvalidInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_subaccount"})
			return
		}
		if strings.Contains(err.Error(), "accounts_active_subaccount_name_idx") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "subaccount_name_in_use"})
			return
		}
		h.logger.Error("subaccount creation failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "subaccount_creation_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *AccountHandler) SubaccountBalances(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	accountID := r.PathValue("account_id")
	if principal.PrincipalType == "subaccount" && accountID != principal.ActiveAccountID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_not_available"})
		return
	}
	items, err := h.service.SubaccountBalances(r.Context(), principal.UserID, accountID)
	if err != nil {
		h.logger.Error("subaccount balances read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "subaccount_balances_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": items})
}

func (h *AccountHandler) SubaccountDetails(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	item, err := h.service.Subaccount(r.Context(), principal.UserID, r.PathValue("account_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subaccount_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *AccountHandler) SubaccountLifecycle(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	status := ""
	switch r.Method {
	case http.MethodDelete:
		status = "deleted"
	case http.MethodPost:
		if r.PathValue("action") == "freeze" {
			status = "frozen"
		} else if r.PathValue("action") == "unfreeze" {
			status = "active"
		}
	}
	if status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_subaccount_action"})
		return
	}
	item, err := h.service.SetSubaccountStatus(r.Context(), principal.UserID, r.PathValue("account_id"), status)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subaccount_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *AccountHandler) Balances(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var balances []accounts.Balance
	var err error
	if principal.PrincipalType == "subaccount" {
		balances, err = h.service.SubaccountBalances(r.Context(), principal.UserID, principal.ActiveAccountID)
	} else {
		balances, err = h.service.Balances(r.Context(), principal.UserID)
	}
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
	if principal.PrincipalType == "subaccount" {
		balances, err := h.service.SubaccountBalances(r.Context(), principal.UserID, principal.ActiveAccountID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "balances_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"funding": []accounts.Balance{}, "uta": []accounts.Balance{}, "subaccounts": balances, "accounts": []any{}})
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
	var items []accounts.TransactionHistoryItem
	var err error
	if principal.PrincipalType == "subaccount" {
		items, err = h.service.AccountTransactionHistory(r.Context(), principal.UserID, principal.ActiveAccountID, limit, cursor)
	} else {
		items, err = h.service.TransactionHistory(r.Context(), principal.UserID, accountKind, limit, cursor)
	}
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
