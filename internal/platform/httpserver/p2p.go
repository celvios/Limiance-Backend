package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/limiance/backend/internal/p2p"
)

type P2PHandler struct {
	service *p2p.Service
	logger  *slog.Logger
}

func NewP2PHandler(service *p2p.Service, logger *slog.Logger) *P2PHandler {
	return &P2PHandler{service: service, logger: logger}
}

func (handler *P2PHandler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	var input p2p.CreateInput
	if !decodeP2PJSON(w, r, &input) {
		return
	}
	input.UserID, input.AccountID, input.IdempotencyKey = principal.UserID, principal.ActiveAccountID, r.Header.Get("Idempotency-Key")
	trade, err := handler.service.Create(r.Context(), input)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	status := http.StatusCreated
	if trade.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, trade)
}

func (handler *P2PHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	limit, offset, ok := p2pPage(w, r)
	if !ok {
		return
	}
	trades, err := handler.service.List(r.Context(), principal.UserID, limit, offset)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": trades, "limit": limit, "offset": offset, "has_more": len(trades) == limit})
}

func (handler *P2PHandler) ListOpen(w http.ResponseWriter, r *http.Request) {
	if _, ok := principalFromContext(r); !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	limit, offset, ok := p2pPage(w, r)
	if !ok {
		return
	}
	trades, err := handler.service.ListOpen(r.Context(), limit, offset)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": trades, "limit": limit, "offset": offset, "has_more": len(trades) == limit})
}

func (handler *P2PHandler) ListDisputes(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	limit, offset, ok := p2pPage(w, r)
	if !ok {
		return
	}
	trades, err := handler.service.ListDisputes(r.Context(), principal.UserID, limit, offset)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disputes": trades, "limit": limit, "offset": offset, "has_more": len(trades) == limit})
}

func p2pPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 50, 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeVersionedError(w, http.StatusBadRequest, "INVALID_FILTER", "limit must be between 1 and 200")
			return 0, 0, false
		}
		limit = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeVersionedError(w, http.StatusBadRequest, "INVALID_FILTER", "offset must be zero or greater")
			return 0, 0, false
		}
		offset = value
	}
	return limit, offset, true
}

func (handler *P2PHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	trade, err := handler.service.Get(r.Context(), principal.UserID, r.PathValue("trade_id"))
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trade)
}

func (handler *P2PHandler) Accept(w http.ResponseWriter, r *http.Request) {
	handler.action(w, r, func(input p2p.ActionInput) (p2p.Trade, error) { return handler.service.Accept(r.Context(), input) })
}
func (handler *P2PHandler) MarkPaid(w http.ResponseWriter, r *http.Request) {
	handler.action(w, r, func(input p2p.ActionInput) (p2p.Trade, error) { return handler.service.MarkPaid(r.Context(), input) })
}
func (handler *P2PHandler) Release(w http.ResponseWriter, r *http.Request) {
	handler.action(w, r, func(input p2p.ActionInput) (p2p.Trade, error) { return handler.service.Release(r.Context(), input) })
}
func (handler *P2PHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	handler.action(w, r, func(input p2p.ActionInput) (p2p.Trade, error) { return handler.service.Cancel(r.Context(), input) })
}

func (handler *P2PHandler) action(w http.ResponseWriter, r *http.Request, execute func(p2p.ActionInput) (p2p.Trade, error)) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	trade, err := execute(p2p.ActionInput{UserID: principal.UserID, AccountID: principal.ActiveAccountID, TradeID: r.PathValue("trade_id"), IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trade)
}

func (handler *P2PHandler) Dispute(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	var input p2p.DisputeInput
	if !decodeP2PJSON(w, r, &input) {
		return
	}
	input.UserID, input.AccountID, input.TradeID, input.IdempotencyKey = principal.UserID, principal.ActiveAccountID, r.PathValue("trade_id"), r.Header.Get("Idempotency-Key")
	trade, err := handler.service.Dispute(r.Context(), input)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trade)
}

func (handler *P2PHandler) Evidence(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	var input p2p.EvidenceInput
	if !decodeP2PJSON(w, r, &input) {
		return
	}
	input.UserID, input.AccountID, input.TradeID, input.IdempotencyKey = principal.UserID, principal.ActiveAccountID, r.PathValue("trade_id"), r.Header.Get("Idempotency-Key")
	trade, err := handler.service.AddEvidence(r.Context(), input)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	status := http.StatusCreated
	if trade.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, trade)
}

func (handler *P2PHandler) ProposeResolution(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	var input p2p.ResolutionInput
	if !decodeP2PJSON(w, r, &input) {
		return
	}
	input.ActorID, input.TradeID, input.IdempotencyKey = principal.UserID, r.PathValue("trade_id"), r.Header.Get("Idempotency-Key")
	resolution, err := handler.service.ProposeResolution(r.Context(), input)
	if err != nil {
		handler.writeError(w, err)
		return
	}
	status := http.StatusCreated
	if resolution.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, resolution)
}

func (handler *P2PHandler) ApproveResolution(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	trade, err := handler.service.ApproveResolution(r.Context(), p2p.ApprovalInput{ActorID: principal.UserID, ResolutionID: r.PathValue("resolution_id"), IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		handler.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trade)
}

func (handler *P2PHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, p2p.ErrIdempotencyRequired):
		writeVersionedError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	case errors.Is(err, p2p.ErrIdempotencyConflict):
		writeVersionedError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "idempotency key was used for a different P2P action")
	case errors.Is(err, p2p.ErrInvalidRequest):
		writeVersionedError(w, http.StatusBadRequest, "INVALID_P2P_REQUEST", "P2P request parameters are invalid")
	case errors.Is(err, p2p.ErrFundingAccountNeeded):
		writeVersionedError(w, http.StatusForbidden, "FUNDING_ACCOUNT_REQUIRED", "switch to an active Funding Account")
	case errors.Is(err, p2p.ErrInsufficientBalance):
		writeVersionedError(w, http.StatusBadRequest, "INSUFFICIENT_BALANCE", "available balance is insufficient for escrow")
	case errors.Is(err, p2p.ErrTradeNotFound), errors.Is(err, p2p.ErrResolutionNotFound):
		writeVersionedError(w, http.StatusNotFound, "P2P_NOT_FOUND", "P2P trade or resolution was not found")
	case errors.Is(err, p2p.ErrInvalidTransition):
		writeVersionedError(w, http.StatusConflict, "INVALID_P2P_STATE", "P2P action is not allowed in the current state")
	case errors.Is(err, p2p.ErrExpired):
		writeVersionedError(w, http.StatusConflict, "P2P_DEADLINE_EXPIRED", "P2P deadline has expired")
	case errors.Is(err, p2p.ErrForbidden):
		writeVersionedError(w, http.StatusForbidden, "P2P_ACTION_FORBIDDEN", "this participant cannot perform the P2P action")
	case errors.Is(err, p2p.ErrApprovalRole):
		writeVersionedError(w, http.StatusForbidden, "P2P_APPROVAL_ROLE_REQUIRED", "the required treasury role is missing")
	case errors.Is(err, p2p.ErrSameApprover):
		writeVersionedError(w, http.StatusConflict, "MAKER_CHECKER_REQUIRED", "the proposer cannot approve the same resolution")
	default:
		handler.logger.Error("P2P operation failed", "error", err)
		writeVersionedError(w, http.StatusServiceUnavailable, "P2P_UNAVAILABLE", "P2P service is temporarily unavailable")
	}
}

func decodeP2PJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return false
	}
	return true
}
