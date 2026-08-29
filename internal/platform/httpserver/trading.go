package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/fees"
	"github.com/limiance/backend/internal/trading"
)

type orderPlacementService interface {
	PlaceOrder(context.Context, trading.PlaceOrderInput) (trading.Order, error)
}

type OrderHandler struct {
	service orderPlacementService
	logger  *slog.Logger
}

func NewOrderHandler(service orderPlacementService, logger *slog.Logger) *OrderHandler {
	return &OrderHandler{service: service, logger: logger}
}

type placeOrderRequest struct {
	Pair        string `json:"pair"`
	Side        string `json:"side"`
	Type        string `json:"type"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
	TimeInForce string `json:"time_in_force"`
	PostOnly    bool   `json:"post_only"`
	ReduceOnly  bool   `json:"reduce_only"`
}

func (handler *OrderHandler) Place(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeVersionedError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
		return
	}
	if principal.PrincipalType == "api_key" && principal.APIKeyScope != "trade" {
		writeVersionedError(w, http.StatusForbidden, "API_KEY_SCOPE_FORBIDDEN", "this API key cannot place orders")
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
		Price: request.Price, Quantity: request.Quantity, TimeInForce: request.TimeInForce,
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
