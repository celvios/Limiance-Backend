package fees

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

func TestRedisFeeCacheTTLAndInvalidation(t *testing.T) {
	redisURL := os.Getenv("LIMIANCE_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set LIMIANCE_TEST_REDIS_URL to run Redis fee cache tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cache, err := NewRedisCache(redisURL)
	if err != nil {
		t.Fatalf("create Redis cache: %v", err)
	}
	defer cache.Close()
	if err = cache.Clear(ctx); err != nil {
		t.Fatalf("clear Redis cache: %v", err)
	}
	want := datamanager.UserFees{TierLevel: 3, MakerFeeBPS: 4, TakerFeeBPS: 8, VolumeUSD: "1000000.00000000"}
	if err = cache.Set(ctx, "user-1:BTCUSDT", want, 50*time.Millisecond); err != nil {
		t.Fatalf("set cached fees: %v", err)
	}
	got, found, err := cache.Get(ctx, "user-1:BTCUSDT")
	if err != nil || !found || got != want {
		t.Fatalf("get cached fees: got=%+v found=%v err=%v", got, found, err)
	}
	if err = cache.Clear(ctx); err != nil {
		t.Fatalf("invalidate Redis cache: %v", err)
	}
	if _, found, err = cache.Get(ctx, "user-1:BTCUSDT"); err != nil || found {
		t.Fatalf("cache key survived invalidation: found=%v err=%v", found, err)
	}
	if err = cache.Set(ctx, "user-1:ETHUSDT", want, 20*time.Millisecond); err != nil {
		t.Fatalf("set expiring cached fees: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, found, err = cache.Get(ctx, "user-1:ETHUSDT"); err != nil || found {
		t.Fatalf("cache key survived TTL: found=%v err=%v", found, err)
	}
}
