package fees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/limiance/backend/internal/datamanager"
	redis "github.com/redis/go-redis/v9"
)

const redisFeePrefix = "limiance:fees:v1:"

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(rawURL string) (*RedisCache, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis fee cache URL: %w", err)
	}
	return &RedisCache{client: redis.NewClient(options)}, nil
}

func (cache *RedisCache) Get(ctx context.Context, key string) (datamanager.UserFees, bool, error) {
	encoded, err := cache.client.Get(ctx, redisFeeKey(key)).Bytes()
	if err == redis.Nil {
		return datamanager.UserFees{}, false, nil
	}
	if err != nil {
		return datamanager.UserFees{}, false, err
	}
	var value datamanager.UserFees
	if err = json.Unmarshal(encoded, &value); err != nil {
		return datamanager.UserFees{}, false, err
	}
	return value, true, nil
}

func (cache *RedisCache) Set(ctx context.Context, key string, value datamanager.UserFees, ttl time.Duration) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return cache.client.Set(ctx, redisFeeKey(key), encoded, ttl).Err()
}

func (cache *RedisCache) Clear(ctx context.Context) error {
	var cursor uint64
	for {
		keys, next, err := cache.client.Scan(ctx, cursor, redisFeePrefix+"*", 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err = cache.client.Unlink(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func (cache *RedisCache) Close() error { return cache.client.Close() }

func redisFeeKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return redisFeePrefix + hex.EncodeToString(hash[:])
}
