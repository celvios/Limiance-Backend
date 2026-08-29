package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type rateLimitDecision struct {
	Allowed   bool
	Remaining int
	ResetAt   time.Time
}
type endpointLimiter interface {
	Allow(context.Context, string, int, time.Duration) (rateLimitDecision, error)
}
type unavailableEndpointLimiter struct{}

func (unavailableEndpointLimiter) Allow(context.Context, string, int, time.Duration) (rateLimitDecision, error) {
	return rateLimitDecision{}, fmt.Errorf("rate limiter is not configured")
}

type redisEndpointLimiter struct {
	client *redis.Client
	prefix string
	now    func() time.Time
}

func newRedisEndpointLimiter(rawURL string) (*redisEndpointLimiter, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis rate-limit URL: %w", err)
	}
	return &redisEndpointLimiter{client: redis.NewClient(options), prefix: "limiance:ratelimit:v1:", now: time.Now}, nil
}

var fixedWindowScript = redis.NewScript(`
local count=redis.call('INCR',KEYS[1])
if count==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end
local ttl=redis.call('PTTL',KEYS[1])
return {count,ttl}
`)

func (limiter *redisEndpointLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (rateLimitDecision, error) {
	hash := sha256.Sum256([]byte(key))
	redisKey := limiter.prefix + hex.EncodeToString(hash[:])
	result, err := fixedWindowScript.Run(ctx, limiter.client, []string{redisKey}, window.Milliseconds()).Slice()
	if err != nil {
		return rateLimitDecision{}, err
	}
	if len(result) != 2 {
		return rateLimitDecision{}, fmt.Errorf("invalid Redis rate-limit response")
	}
	count, ok := result[0].(int64)
	if !ok {
		return rateLimitDecision{}, fmt.Errorf("invalid Redis rate-limit count")
	}
	ttl, ok := result[1].(int64)
	if !ok {
		return rateLimitDecision{}, fmt.Errorf("invalid Redis rate-limit TTL")
	}
	remaining := limit - int(count)
	if remaining < 0 {
		remaining = 0
	}
	if ttl < 0 {
		ttl = 0
	}
	return rateLimitDecision{Allowed: count <= int64(limit), Remaining: remaining, ResetAt: limiter.now().Add(time.Duration(ttl) * time.Millisecond)}, nil
}
func (limiter *redisEndpointLimiter) Close() error { return limiter.client.Close() }

func endpointRateLimit(limiter endpointLimiter, bucket string, limit int, window time.Duration, required bool, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if identity == "" {
			identity = remoteIP(r)
		}
		decision, err := limiter.Allow(r.Context(), bucket+":"+identity, limit, window)
		if err != nil {
			if required {
				writeVersionedError(w, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "rate limit service is temporarily unavailable")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.ResetAt.Unix(), 10))
		if !decision.Allowed {
			w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(time.Until(decision.ResetAt).Seconds())), 10))
			writeVersionedError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "request rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}
