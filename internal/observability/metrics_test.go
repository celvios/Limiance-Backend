package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareExposesRequestMetrics(t *testing.T) {
	metrics := NewMetrics()
	handler := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/withdrawals/123e4567-e89b-12d3-a456-426614174000", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, `limiance_http_requests_total{method="POST",path="/v1/withdrawals/:id",status="201"}`) {
		t.Fatalf("request counter missing from metrics: %s", body)
	}
}
