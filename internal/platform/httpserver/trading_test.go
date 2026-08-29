package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/trading"
)

type orderPlacementStub struct {
	result trading.Order
	orders []trading.Order
	err    error
	input  trading.PlaceOrderInput
	filter trading.OrderFilter
	cancel trading.CancelOrderInput
}

func (stub *orderPlacementStub) PlaceOrder(_ context.Context, input trading.PlaceOrderInput) (trading.Order, error) {
	stub.input = input
	return stub.result, stub.err
}

func (stub *orderPlacementStub) ListOrders(_ context.Context, _, _ string, filter trading.OrderFilter) ([]trading.Order, error) {
	stub.filter = filter
	return stub.orders, stub.err
}

func (stub *orderPlacementStub) GetOrder(_ context.Context, _, _, _ string) (trading.Order, error) {
	return stub.result, stub.err
}

func (stub *orderPlacementStub) CancelOrder(_ context.Context, input trading.CancelOrderInput) (trading.Order, error) {
	stub.cancel = input
	return stub.result, stub.err
}

func TestOrderLifecyclePlaceOrder(t *testing.T) {
	t.Run("creates an authenticated order", func(t *testing.T) {
		service := &orderPlacementStub{result: trading.Order{ID: "order-1", Status: "OPEN"}}
		handler := NewOrderHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
		request := authenticatedOrderRequest(`{"pair":"BTCUSDT","side":"BUY","type":"LIMIT","price":"5000000000000","quantity":"1000000","time_in_force":"GTC"}`, auth.Principal{
			UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta", PrincipalType: "session",
		})
		request.Header.Set("Idempotency-Key", "request-1")
		response := httptest.NewRecorder()
		handler.Place(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("got status %d: %s", response.Code, response.Body.String())
		}
		if service.input.UserID != "user-1" || service.input.AccountID != "account-1" || service.input.IdempotencyKey != "request-1" {
			t.Fatalf("principal or idempotency not forwarded: %#v", service.input)
		}
		var body trading.Order
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.ID != "order-1" || body.Status != "OPEN" {
			t.Fatalf("unexpected response: body=%#v err=%v", body, err)
		}
	})

	t.Run("returns existing duplicate", func(t *testing.T) {
		service := &orderPlacementStub{result: trading.Order{ID: "order-1", Status: "OPEN", Duplicate: true}}
		handler := NewOrderHandler(service, slog.Default())
		request := authenticatedOrderRequest(validOrderJSON(), auth.Principal{UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta"})
		response := httptest.NewRecorder()
		handler.Place(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("got status %d", response.Code)
		}
	})

	t.Run("rejects missing authentication", func(t *testing.T) {
		handler := NewOrderHandler(&orderPlacementStub{}, slog.Default())
		response := httptest.NewRecorder()
		handler.Place(response, httptest.NewRequest(http.MethodPost, "/v2/orders", bytes.NewBufferString(validOrderJSON())))
		assertErrorCode(t, response, http.StatusUnauthorized, "UNAUTHORIZED")
	})

	t.Run("rejects read only API key", func(t *testing.T) {
		handler := NewOrderHandler(&orderPlacementStub{}, slog.Default())
		request := authenticatedOrderRequest(validOrderJSON(), auth.Principal{UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta", PrincipalType: "api_key", APIKeyScope: "read_only"})
		response := httptest.NewRecorder()
		handler.Place(response, request)
		assertErrorCode(t, response, http.StatusForbidden, "INSUFFICIENT_SCOPE")
	})

	t.Run("maps insufficient balance", func(t *testing.T) {
		handler := NewOrderHandler(&orderPlacementStub{err: trading.ErrInsufficientBalance}, slog.Default())
		request := authenticatedOrderRequest(validOrderJSON(), auth.Principal{UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta"})
		response := httptest.NewRecorder()
		handler.Place(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, "INSUFFICIENT_BALANCE")
	})

	t.Run("rejects unknown request fields", func(t *testing.T) {
		handler := NewOrderHandler(&orderPlacementStub{}, slog.Default())
		request := authenticatedOrderRequest(`{"pair":"BTCUSDT","unknown":true}`, auth.Principal{UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta"})
		response := httptest.NewRecorder()
		handler.Place(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, "INVALID_REQUEST")
	})
}

func TestOrderLifecycleReadAndCancel(t *testing.T) {
	principal := auth.Principal{UserID: "user-1", ActiveAccountID: "account-1", ActiveAccountKind: "uta", PrincipalType: "session"}

	t.Run("lists only requested open orders", func(t *testing.T) {
		service := &orderPlacementStub{orders: []trading.Order{{ID: "order-1", Status: "OPEN"}}}
		handler := NewOrderHandler(service, slog.Default())
		request := httptest.NewRequest(http.MethodGet, "/v2/orders?status=OPEN&limit=20", nil)
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		response := httptest.NewRecorder()
		handler.List(response, request)
		if response.Code != http.StatusOK || service.filter.Status != "OPEN" || service.filter.Limit != 20 {
			t.Fatalf("unexpected list result: status=%d filter=%#v body=%s", response.Code, service.filter, response.Body.String())
		}
	})

	t.Run("returns order details", func(t *testing.T) {
		service := &orderPlacementStub{result: trading.Order{ID: "order-1", Status: "PARTIALLY_FILLED", FilledQuantity: "5", RemainingQuantity: "5", AvgPrice: "100"}}
		handler := NewOrderHandler(service, slog.Default())
		request := httptest.NewRequest(http.MethodGet, "/v2/orders/order-1", nil)
		request.SetPathValue("order_id", "order-1")
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		response := httptest.NewRecorder()
		handler.Get(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"filled_quantity":"5"`)) {
			t.Fatalf("unexpected get result: status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("cancels with idempotency", func(t *testing.T) {
		service := &orderPlacementStub{result: trading.Order{ID: "order-1", Status: "CANCELED"}}
		handler := NewOrderHandler(service, slog.Default())
		request := httptest.NewRequest(http.MethodDelete, "/v2/orders/order-1", nil)
		request.SetPathValue("order_id", "order-1")
		request.Header.Set("Idempotency-Key", "cancel-1")
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		response := httptest.NewRecorder()
		handler.Cancel(response, request)
		if response.Code != http.StatusOK || service.cancel.OrderID != "order-1" || service.cancel.IdempotencyKey != "cancel-1" {
			t.Fatalf("unexpected cancel result: status=%d input=%#v body=%s", response.Code, service.cancel, response.Body.String())
		}
	})

	t.Run("maps filled cancellation", func(t *testing.T) {
		handler := NewOrderHandler(&orderPlacementStub{err: trading.ErrOrderAlreadyFilled}, slog.Default())
		request := httptest.NewRequest(http.MethodDelete, "/v2/orders/order-1", nil)
		request.SetPathValue("order_id", "order-1")
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		response := httptest.NewRecorder()
		handler.Cancel(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, "ORDER_ALREADY_FILLED")
	})
}

func TestOrderLifecycleDefaultListIncludesAllActiveStates(t *testing.T) {
	service := &orderPlacementStub{orders: []trading.Order{{ID: "order-1", Status: "PARTIALLY_FILLED"}}}
	handler := NewOrderHandler(service, slog.Default())
	request := httptest.NewRequest(http.MethodGet, "/v2/orders", nil)
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, auth.Principal{UserID: "user-1", ActiveAccountID: "account-1"}))
	response := httptest.NewRecorder()
	handler.List(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if service.filter.Status != "" || service.filter.History {
		t.Fatalf("default active-order filter was narrowed unexpectedly: %#v", service.filter)
	}
}

func authenticatedOrderRequest(body string, principal auth.Principal) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v2/orders", bytes.NewBufferString(body))
	return request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
}

func validOrderJSON() string {
	return `{"pair":"BTCUSDT","side":"BUY","type":"LIMIT","price":"5000000000000","quantity":"1000000","time_in_force":"GTC"}`
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("got status %d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != code {
		t.Fatalf("got code %q, want %q", body.Error.Code, code)
	}
}
