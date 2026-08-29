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
	err    error
	input  trading.PlaceOrderInput
}

func (stub *orderPlacementStub) PlaceOrder(_ context.Context, input trading.PlaceOrderInput) (trading.Order, error) {
	stub.input = input
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
		assertErrorCode(t, response, http.StatusForbidden, "API_KEY_SCOPE_FORBIDDEN")
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
