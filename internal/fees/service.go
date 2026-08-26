package fees

import (
	"context"
	"sync"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

type Repository interface {
	UserFees(context.Context, string) (datamanager.UserFees, error)
	RefreshUserFeeTiers(context.Context) error
}

type Cache interface {
	Get(string) (datamanager.UserFees, bool)
	Set(string, datamanager.UserFees, time.Duration)
}

type cacheEntry struct {
	fees      datamanager.UserFees
	expiresAt time.Time
}

type memoryCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func newMemoryCache() *memoryCache { return &memoryCache{entries: make(map[string]cacheEntry)} }

func (c *memoryCache) Get(key string) (datamanager.UserFees, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			c.mu.Lock()
			delete(c.entries, key)
			c.mu.Unlock()
		}
		return datamanager.UserFees{}, false
	}
	return entry.fees, true
}

func (c *memoryCache) Set(key string, fees datamanager.UserFees, ttl time.Duration) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{fees: fees, expiresAt: time.Now().Add(ttl)}
	c.mu.Unlock()
}

type Service struct {
	repository Repository
	cache      Cache
}

func NewService(repository Repository, cache Cache) *Service {
	if cache == nil {
		cache = newMemoryCache()
	}
	return &Service{repository: repository, cache: cache}
}

func (s *Service) GetUserFees(ctx context.Context, userID, pair string) (datamanager.UserFees, error) {
	key := userID + ":" + pair
	if fees, ok := s.cache.Get(key); ok {
		return fees, nil
	}
	fees, err := s.repository.UserFees(ctx, userID)
	if err != nil {
		return datamanager.UserFees{}, err
	}
	s.cache.Set(key, fees, 5*time.Minute)
	return fees, nil
}

func (s *Service) RefreshUserFeeTiers(ctx context.Context) error {
	return s.repository.RefreshUserFeeTiers(ctx)
}
