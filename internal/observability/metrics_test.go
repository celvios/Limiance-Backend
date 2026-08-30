package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMiddlewareExposesRequestMetrics(t *testing.T) {
	metrics := NewMetrics()
	handler := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/withdrawals/123e4567-e89b-12d3-a456-426614174000", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, `limiance_http_requests_total{method="POST",path="/v1/withdrawals/:id",status="502"}`) {
		t.Fatalf("request counter missing from metrics: %s", body)
	}
	if !strings.Contains(body, `limiance_http_errors_total{method="POST",path="/v1/withdrawals/:id",status="502"}`) {
		t.Fatalf("error counter missing from metrics: %s", body)
	}
}

func TestMiddlewarePreservesWebSocketUpgrade(t *testing.T) {
	metrics := NewMetrics()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = connection.Close()
	})))
	defer server.Close()

	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("WebSocket upgrade through metrics middleware: status=%d err=%v", status, err)
	}
	_ = connection.Close()
}

func TestDatabaseCollectorExposesOperationalMetrics(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run database metrics test")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	metrics := NewMetricsWithDB(pool)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, metric := range []string{"limiance_active_sessions", "limiance_sqs_queue_backlog", "limiance_ledger_imbalanced_groups"} {
		if !strings.Contains(body, metric) {
			t.Fatalf("database collector did not expose %s: %s", metric, body)
		}
	}
}

func TestRecordWebhookReceiptMetric(t *testing.T) {
	metrics := NewMetrics()
	metrics.RecordWebhookReceipt("fireblocks", "processed")
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, `limiance_webhook_receipts_total{provider="fireblocks",status="processed"}`) {
		t.Fatalf("webhook receipt metric missing from metrics: %s", body)
	}
}
