package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type endpointLimiterStub struct {
	calls int
	err   error
}

func (stub *endpointLimiterStub) Allow(_ context.Context, _ string, limit int, _ time.Duration) (rateLimitDecision, error) {
	stub.calls++
	return rateLimitDecision{Allowed: stub.calls <= limit, Remaining: max(0, limit-stub.calls), ResetAt: time.Now().Add(time.Minute)}, stub.err
}

func TestEndpointRateLimitRejectsExcessRequests(t *testing.T) {
	limiter := &endpointLimiterStub{}
	handler := endpointRateLimit(limiter, "orders.write", 2, time.Minute, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for attempt, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v2/orders", nil))
		if response.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", attempt, response.Code, want)
		}
	}
}

func TestRequiredEndpointRateLimitFailsClosed(t *testing.T) {
	handler := endpointRateLimit(unavailableEndpointLimiter{}, "orders.write", 1, time.Minute, true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unavailable limiter reached handler") }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v2/orders", nil))
	assertErrorCode(t, response, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE")
}

func TestRedisEndpointRateLimitIsShared(t *testing.T) {
	redisURL := os.Getenv("LIMIANCE_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set LIMIANCE_TEST_REDIS_URL to run Redis rate-limit tests")
	}
	limiter, err := newRedisEndpointLimiter(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := "test:" + time.Now().UTC().Format(time.RFC3339Nano)
	first, err := limiter.Allow(ctx, key, 2, time.Minute)
	if err != nil || !first.Allowed || first.Remaining != 1 {
		t.Fatalf("first decision=%+v err=%v", first, err)
	}
	second, err := limiter.Allow(ctx, key, 2, time.Minute)
	if err != nil || !second.Allowed || second.Remaining != 0 {
		t.Fatalf("second decision=%+v err=%v", second, err)
	}
	third, err := limiter.Allow(ctx, key, 2, time.Minute)
	if err != nil || third.Allowed {
		t.Fatalf("third decision=%+v err=%v", third, err)
	}
}
