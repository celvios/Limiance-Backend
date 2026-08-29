package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/fees"
	"github.com/limiance/backend/internal/trading"
)

type orderPlacementService interface {
	PlaceOrder(context.Context, trading.PlaceOrderInput) (trading.Order, error)
	ListOrders(context.Context, string, string, trading.OrderFilter) ([]trading.Order, error)
	GetOrder(context.Context, string, string, string) (trading.Order, error)
	CancelOrder(context.Context, trading.CancelOrderInput) (trading.Order, error)
}

type OrderHandler struct {
	service orderPlacementService
	logger  *slog.Logger
}

func NewOrderHandler(service orderPlacementService, logger *slog.Logger) *OrderHandler {
	return &OrderHandler{service: service, logger: logger}
}

type placeOrderRequest struct {
	Pair         string `json:"pair"`
	Side         string `json:"side"`
	Type         string `json:"type"`
	Price        string `json:"price"`
	TriggerPrice string `json:"trigger_price"`
	Quantity     string `json:"quantity"`
	TimeInForce  string `json:"time_in_force"`
	PostOnly     bool   `json:"post_only"`
	ReduceOnly   bool   `json:"reduce_only"`
}

func (handler *OrderHandler) Place(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	if principal.PrincipalType == "api_key" && principal.APIKeyScope != "trade" {
		writeVersionedError(w, http.StatusForbidden, "INSUFFICIENT_SCOPE", "this API key cannot place orders")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var request placeOrderRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body must contain one JSON object")
		return
	}
	order, err := handler.service.PlaceOrder(r.Context(), trading.PlaceOrderInput{
		UserID: principal.UserID, AccountID: principal.ActiveAccountID, AccountKind: principal.ActiveAccountKind,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Pair: request.Pair, Side: request.Side, Type: request.Type,
		Price: request.Price, TriggerPrice: request.TriggerPrice, Quantity: request.Quantity, TimeInForce: request.TimeInForce,
		PostOnly: request.PostOnly, ReduceOnly: request.ReduceOnly,
	})
	if err != nil {
		handler.writePlaceError(w, err)
		return
	}
	status := http.StatusCreated
	if order.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, order)
}

func (handler *OrderHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	filter, err := orderFilterFromRequest(r, false)
	if err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_FILTER", err.Error())
		return
	}
	orders, err := handler.service.ListOrders(r.Context(), principal.UserID, principal.ActiveAccountID, filter)
	if err != nil {
		handler.writePlaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "limit": filter.Limit, "offset": filter.Offset, "has_more": len(orders) == filter.Limit})
}

func (handler *OrderHandler) History(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	filter, err := orderFilterFromRequest(r, true)
	if err != nil {
		writeVersionedError(w, http.StatusBadRequest, "INVALID_FILTER", err.Error())
		return
	}
	orders, err := handler.service.ListOrders(r.Context(), principal.UserID, principal.ActiveAccountID, filter)
	if err != nil {
		handler.writePlaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "limit": filter.Limit, "offset": filter.Offset, "has_more": len(orders) == filter.Limit})
}

func (handler *OrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	order, err := handler.service.GetOrder(r.Context(), principal.UserID, principal.ActiveAccountID, strings.TrimSpace(r.PathValue("order_id")))
	if err != nil {
		handler.writePlaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (handler *OrderHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	if principal.PrincipalType == "api_key" && principal.APIKeyScope != "trade" {
		writeVersionedError(w, http.StatusForbidden, "INSUFFICIENT_SCOPE", "this API key cannot cancel orders")
		return
	}
	order, err := handler.service.CancelOrder(r.Context(), trading.CancelOrderInput{
		UserID: principal.UserID, AccountID: principal.ActiveAccountID, OrderID: strings.TrimSpace(r.PathValue("order_id")),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		handler.writePlaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func orderFilterFromRequest(r *http.Request, history bool) (trading.OrderFilter, error) {
	filter := trading.OrderFilter{Status: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status"))), Pair: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("pair"))), History: history, Limit: 50}
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			return trading.OrderFilter{}, errors.New("limit must be between 1 and 200")
		}
		filter.Limit = parsed
	}
	if value := strings.TrimSpace(r.URL.Query().Get("offset")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return trading.OrderFilter{}, errors.New("offset must be zero or greater")
		}
		filter.Offset = parsed
	}
	return filter, nil
}

func (handler *OrderHandler) writePlaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, trading.ErrIdempotencyRequired):
		writeVersionedError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	case errors.Is(err, trading.ErrIdempotencyConflict):
		writeVersionedError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "idempotency key was used for a different order")
	case errors.Is(err, trading.ErrInsufficientBalance):
		writeVersionedError(w, http.StatusBadRequest, "INSUFFICIENT_BALANCE", "available balance is insufficient")
	case errors.Is(err, trading.ErrTradingAccountRequired):
		writeVersionedError(w, http.StatusForbidden, "TRADING_ACCOUNT_REQUIRED", "switch to a unified trading account or subaccount")
	case errors.Is(err, trading.ErrPairUnavailable):
		writeVersionedError(w, http.StatusBadRequest, "PAIR_UNAVAILABLE", "trading pair is unavailable")
	case errors.Is(err, trading.ErrInvalidOrder):
		writeVersionedError(w, http.StatusBadRequest, "INVALID_ORDER", "order parameters are invalid")
	case errors.Is(err, trading.ErrEngineUnavailable):
		writeVersionedError(w, http.StatusServiceUnavailable, "MATCHING_ENGINE_UNAVAILABLE", "matching engine is temporarily unavailable")
	case errors.Is(err, trading.ErrEngineResponse):
		writeVersionedError(w, http.StatusBadGateway, "MATCHING_ENGINE_RESPONSE_INVALID", "matching engine response was invalid")
	case errors.Is(err, trading.ErrOrderNotFound):
		writeVersionedError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order was not found")
	case errors.Is(err, trading.ErrOrderAlreadyFilled):
		writeVersionedError(w, http.StatusBadRequest, "ORDER_ALREADY_FILLED", "filled orders cannot be canceled")
	case errors.Is(err, trading.ErrOrderNotCancelable):
		writeVersionedError(w, http.StatusBadRequest, "ORDER_NOT_CANCELABLE", "order cannot be canceled in its current state")
	default:
		handler.logger.Error("order placement failed", "error", err)
		writeVersionedError(w, http.StatusServiceUnavailable, "ORDER_UNAVAILABLE", "order service is temporarily unavailable")
	}
}

type cachedTradingFeeResolver struct {
	service *fees.Service
}

func (resolver cachedTradingFeeResolver) Resolve(ctx context.Context, userID, pair string) (trading.FeeTier, error) {
	resolved, err := resolver.service.GetUserFees(ctx, userID, pair)
	if err != nil {
		return trading.FeeTier{}, err
	}
	return feeTierFromStored(resolved), nil
}

func feeTierFromStored(stored datamanager.UserFees) trading.FeeTier {
	return trading.FeeTier{Level: stored.TierLevel, MakerFeeBPS: stored.MakerFeeBPS, TakerFeeBPS: stored.TakerFeeBPS}
}
