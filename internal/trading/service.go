package trading

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/limiance/backend/internal/trading/protocol"
)

const atomicScale = uint64(100000000)

type Service struct {
	store      Store
	fees       FeeResolver
	engine     Engine
	control    Engine
	now        func() time.Time
	newOrderID func() (string, error)
}

func NewService(store Store, fees FeeResolver, engine Engine) *Service {
	return &Service{store: store, fees: fees, engine: engine, control: engine, now: time.Now, newOrderID: randomUUID}
}

func (service *Service) WithControlEngine(engine Engine) *Service {
	service.control = engine
	return service
}

func (service *Service) PlaceOrder(ctx context.Context, input PlaceOrderInput) (Order, error) {
	normalized, price, triggerPrice, quantity, err := normalizeOrder(input)
	if err != nil {
		return Order{}, err
	}
	feeTier, err := service.fees.Resolve(ctx, normalized.UserID, normalized.Pair)
	if err != nil {
		return Order{}, fmt.Errorf("resolve fee tier: %w", err)
	}
	if feeTier.Level < 0 || feeTier.Level > math.MaxUint8 || feeTier.MakerFeeBPS < math.MinInt16 || feeTier.MakerFeeBPS > math.MaxInt16 || feeTier.TakerFeeBPS < math.MinInt16 || feeTier.TakerFeeBPS > math.MaxInt16 {
		return Order{}, fmt.Errorf("%w: fee tier is outside the wire contract", ErrInvalidOrder)
	}
	engineOrderType := effectiveOrderType(normalized.Type)
	holdAmount, holdAll, err := reservation(normalized.Side, engineOrderType, price, quantity, feeTier.TakerFeeBPS)
	if err != nil {
		return Order{}, err
	}
	orderID, err := service.newOrderID()
	if err != nil {
		return Order{}, fmt.Errorf("generate order id: %w", err)
	}
	ingress := protocol.OrderIngress{
		ID:          orderID,
		UserID:      normalized.UserID,
		Pair:        normalized.Pair,
		Side:        protocolSide(normalized.Side),
		OrderType:   protocolOrderType(engineOrderType),
		Price:       price,
		Quantity:    quantity,
		TimeInForce: protocolTimeInForce(normalized.TimeInForce),
		PostOnly:    normalized.PostOnly,
		ReduceOnly:  normalized.ReduceOnly,
		TimestampNS: uint64(service.now().UTC().UnixNano()),
		FeeTier:     uint8(feeTier.Level),
	}
	payload, err := protocol.EncodeOrderIngress(ingress)
	if err != nil {
		return Order{}, fmt.Errorf("encode order ingress: %w", err)
	}
	command := CreateOrderCommand{
		OrderID: orderID, UserID: normalized.UserID, AccountID: normalized.AccountID,
		Pair: normalized.Pair, Side: normalized.Side, Type: normalized.Type, Price: price, Quantity: quantity,
		TimeInForce: normalized.TimeInForce, PostOnly: normalized.PostOnly, ReduceOnly: normalized.ReduceOnly,
		FeeTier: feeTier.Level, IdempotencyKey: normalized.IdempotencyKey, RequestHash: requestHash(normalized),
		HoldAmount: holdAmount, HoldAllAvailable: holdAll, Conditional: isConditionalType(normalized.Type),
		TriggerPrice: triggerPrice, TriggerDirection: triggerDirection(normalized.Type, normalized.Side), EnginePayload: payload,
	}
	order, err := service.store.CreateOrder(ctx, command)
	if err != nil {
		return Order{}, err
	}
	if order.Status != "PENDING" {
		return order, nil
	}
	return service.dispatchOrder(ctx, order)
}

func (service *Service) ListOrders(ctx context.Context, userID, accountID string, filter OrderFilter) ([]Order, error) {
	filter.Status = strings.ToUpper(strings.TrimSpace(filter.Status))
	filter.Pair = strings.ToUpper(strings.TrimSpace(filter.Pair))
	if filter.Status != "" && !validOrderStatus(filter.Status) {
		return nil, fmt.Errorf("%w: status filter is invalid", ErrInvalidOrder)
	}
	if filter.Pair != "" && !validPair(filter.Pair) {
		return nil, fmt.Errorf("%w: pair filter is invalid", ErrInvalidOrder)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalidOrder)
	}
	return service.store.ListOrders(ctx, strings.TrimSpace(userID), strings.TrimSpace(accountID), filter)
}

func (service *Service) GetOrder(ctx context.Context, userID, accountID, orderID string) (Order, error) {
	if strings.TrimSpace(orderID) == "" {
		return Order{}, ErrOrderNotFound
	}
	return service.store.GetOrder(ctx, strings.TrimSpace(userID), strings.TrimSpace(accountID), strings.TrimSpace(orderID))
}

func (service *Service) CancelOrder(ctx context.Context, input CancelOrderInput) (Order, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.OrderID = strings.TrimSpace(input.OrderID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" {
		return Order{}, ErrIdempotencyRequired
	}
	if input.UserID == "" || input.AccountID == "" || input.OrderID == "" || len(input.IdempotencyKey) > 255 {
		return Order{}, ErrInvalidOrder
	}
	commandID, err := randomUUID()
	if err != nil {
		return Order{}, err
	}
	existing, err := service.store.GetOrder(ctx, input.UserID, input.AccountID, input.OrderID)
	if err != nil {
		return Order{}, err
	}
	payload, err := protocol.EncodeCancelOrderCommand(protocol.CancelOrderCommand{
		CommandID: commandID, OrderID: input.OrderID, Pair: existing.Pair, TimestampNS: uint64(service.now().UTC().UnixNano()),
	})
	if err != nil {
		return Order{}, err
	}
	order, err := service.store.PrepareCancel(ctx, CancelOrderCommand{
		UserID: input.UserID, AccountID: input.AccountID, OrderID: input.OrderID,
		CommandID: commandID, IdempotencyKey: input.IdempotencyKey, Payload: payload,
	})
	if err != nil || order.Status == "CANCELED" {
		return order, err
	}
	if service.control == nil {
		return Order{}, ErrEngineUnavailable
	}
	reply, err := service.control.Request(payload)
	if err != nil {
		_ = service.store.RecordDispatchFailure(ctx, order.ID, err.Error())
		return Order{}, fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	}
	ack, err := protocol.DecodeControlAck(reply)
	if err != nil || ack.CommandID != commandID || !ack.Accepted {
		if err == nil {
			err = fmt.Errorf("control command rejected: %s", ack.Reason)
		}
		return Order{}, fmt.Errorf("%w: %v", ErrEngineResponse, err)
	}
	return service.store.ApplyCancelAck(ctx, input.UserID, input.OrderID, ack)
}

func (service *Service) TriggerConditionalOrders(ctx context.Context, pair string, markPrice uint64, limit int) ([]Order, error) {
	if markPrice == 0 || !validPair(strings.ToUpper(strings.TrimSpace(pair))) {
		return nil, ErrInvalidOrder
	}
	orders, err := service.store.ActivateConditionalOrders(ctx, strings.ToUpper(strings.TrimSpace(pair)), markPrice, limit)
	if err != nil {
		return nil, err
	}
	for index := range orders {
		orders[index], err = service.dispatchOrder(ctx, orders[index])
		if err != nil {
			return orders[:index], err
		}
	}
	return orders, nil
}

func (service *Service) dispatchOrder(ctx context.Context, order Order) (Order, error) {
	if service.engine == nil {
		return Order{}, ErrEngineUnavailable
	}
	reply, err := service.engine.Request(order.EnginePayload)
	if err != nil {
		_ = service.store.RecordDispatchFailure(ctx, order.ID, err.Error())
		return Order{}, fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	}
	status, err := protocol.DecodeOrderStatusEvent(reply)
	if err != nil || status.OrderID != order.ID {
		reason := "order acknowledgement did not match the request"
		if err != nil {
			reason = err.Error()
		}
		_ = service.store.RecordDispatchFailure(ctx, order.ID, reason)
		return Order{}, fmt.Errorf("%w: %s", ErrEngineResponse, reason)
	}
	updated, err := service.store.ApplyEngineStatus(ctx, order.ID, status)
	if err != nil {
		return Order{}, err
	}
	updated.Duplicate = order.Duplicate
	return updated, nil
}

func normalizeOrder(input PlaceOrderInput) (PlaceOrderInput, uint64, uint64, uint64, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.AccountKind = strings.ToLower(strings.TrimSpace(input.AccountKind))
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Pair = strings.ToUpper(strings.TrimSpace(input.Pair))
	input.Side = strings.ToUpper(strings.TrimSpace(input.Side))
	input.Type = strings.ToUpper(strings.TrimSpace(input.Type))
	input.TimeInForce = strings.ToUpper(strings.TrimSpace(input.TimeInForce))
	if input.UserID == "" || input.AccountID == "" {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: account identity is required", ErrInvalidOrder)
	}
	if input.AccountKind != "uta" && input.AccountKind != "subaccount" {
		return PlaceOrderInput{}, 0, 0, 0, ErrTradingAccountRequired
	}
	if input.IdempotencyKey == "" {
		return PlaceOrderInput{}, 0, 0, 0, ErrIdempotencyRequired
	}
	if len(input.IdempotencyKey) > 255 {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: idempotency key is too long", ErrInvalidOrder)
	}
	if !validPair(input.Pair) {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: pair is invalid", ErrInvalidOrder)
	}
	if input.Side != "BUY" && input.Side != "SELL" {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: side is invalid", ErrInvalidOrder)
	}
	if input.Type != "LIMIT" && input.Type != "MARKET" && input.Type != "STOP_LIMIT" && input.Type != "STOP_MARKET" && input.Type != "TAKE_PROFIT_LIMIT" {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: type is invalid", ErrInvalidOrder)
	}
	if input.TimeInForce == "" {
		if effectiveOrderType(input.Type) == "MARKET" {
			input.TimeInForce = "IOC"
		} else {
			input.TimeInForce = "GTC"
		}
	}
	if input.TimeInForce != "GTC" && input.TimeInForce != "IOC" && input.TimeInForce != "FOK" {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: time_in_force is invalid", ErrInvalidOrder)
	}
	if input.PostOnly && effectiveOrderType(input.Type) != "LIMIT" {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: post_only requires a limit order", ErrInvalidOrder)
	}
	price, err := parseAtomic(input.Price, effectiveOrderType(input.Type) == "MARKET")
	if err != nil {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: price is invalid", ErrInvalidOrder)
	}
	if effectiveOrderType(input.Type) == "MARKET" && price != 0 {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: market orders cannot specify price", ErrInvalidOrder)
	}
	triggerPrice, err := parseAtomic(input.TriggerPrice, !isConditionalType(input.Type))
	if err != nil || isConditionalType(input.Type) && triggerPrice == 0 || !isConditionalType(input.Type) && triggerPrice != 0 {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: trigger_price is invalid", ErrInvalidOrder)
	}
	quantity, err := parseAtomic(input.Quantity, false)
	if err != nil {
		return PlaceOrderInput{}, 0, 0, 0, fmt.Errorf("%w: quantity is invalid", ErrInvalidOrder)
	}
	input.Price = strconv.FormatUint(price, 10)
	input.TriggerPrice = strconv.FormatUint(triggerPrice, 10)
	input.Quantity = strconv.FormatUint(quantity, 10)
	return input, price, triggerPrice, quantity, nil
}

func isConditionalType(orderType string) bool {
	return orderType == "STOP_LIMIT" || orderType == "STOP_MARKET" || orderType == "TAKE_PROFIT_LIMIT"
}

func effectiveOrderType(orderType string) string {
	if orderType == "STOP_MARKET" {
		return "MARKET"
	}
	if isConditionalType(orderType) {
		return "LIMIT"
	}
	return orderType
}

func triggerDirection(orderType, side string) string {
	if !isConditionalType(orderType) {
		return ""
	}
	if orderType == "TAKE_PROFIT_LIMIT" {
		if side == "BUY" {
			return "DOWN"
		}
		return "UP"
	}
	if side == "BUY" {
		return "UP"
	}
	return "DOWN"
}

func parseAtomic(value string, allowEmpty bool) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" && allowEmpty {
		return 0, nil
	}
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, ErrInvalidOrder
	}
	amount, err := strconv.ParseUint(value, 10, 64)
	if err != nil || amount == 0 && !allowEmpty {
		return 0, ErrInvalidOrder
	}
	return amount, nil
}

func reservation(side, orderType string, price, quantity uint64, takerFeeBPS int) (string, bool, error) {
	if side == "SELL" {
		return strconv.FormatUint(quantity, 10), false, nil
	}
	if orderType == "MARKET" {
		return "", true, nil
	}
	notional := ceilProductDiv(price, quantity, atomicScale)
	fee := new(big.Int)
	if takerFeeBPS > 0 {
		fee = ceilBigProductDiv(notional, big.NewInt(int64(takerFeeBPS)), big.NewInt(10000))
	}
	total := new(big.Int).Add(notional, fee)
	if total.Sign() <= 0 || total.BitLen() > 64 {
		return "", false, fmt.Errorf("%w: reservation exceeds unsigned 64-bit atomic units", ErrInvalidOrder)
	}
	return total.String(), false, nil
}

func ceilProductDiv(left, right, divisor uint64) *big.Int {
	return ceilBigProductDiv(new(big.Int).SetUint64(left), new(big.Int).SetUint64(right), new(big.Int).SetUint64(divisor))
}

func ceilBigProductDiv(left, right, divisor *big.Int) *big.Int {
	product := new(big.Int).Mul(left, right)
	quotient, remainder := new(big.Int).QuoRem(product, divisor, new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func requestHash(input PlaceOrderInput) [32]byte {
	canonical := strings.Join([]string{input.UserID, input.AccountID, input.Pair, input.Side, input.Type, input.Price, input.TriggerPrice, input.Quantity, input.TimeInForce, strconv.FormatBool(input.PostOnly), strconv.FormatBool(input.ReduceOnly)}, "\x1f")
	return sha256.Sum256([]byte(canonical))
}

func validPair(value string) bool {
	if len(value) < 6 || len(value) > 20 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func validOrderStatus(value string) bool {
	switch value {
	case "CONDITIONAL", "PENDING", "PENDING_CANCEL", "OPEN", "PARTIALLY_FILLED", "FILLED", "CANCELED", "REJECTED":
		return true
	default:
		return false
	}
}

func protocolSide(value string) protocol.OrderSide {
	if value == "SELL" {
		return protocol.OrderSideSell
	}
	return protocol.OrderSideBuy
}

func protocolOrderType(value string) protocol.OrderType {
	switch value {
	case "MARKET":
		return protocol.OrderTypeMarket
	case "STOP_LIMIT":
		return protocol.OrderTypeStopLimit
	default:
		return protocol.OrderTypeLimit
	}
}

func protocolTimeInForce(value string) protocol.TimeInForce {
	switch value {
	case "IOC":
		return protocol.TimeInForceIOC
	case "FOK":
		return protocol.TimeInForceFOK
	default:
		return protocol.TimeInForceGTC
	}
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
