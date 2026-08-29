package fees

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidPolicy = errors.New("fee policy is invalid")

var decimal24Scale8 = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,8})?$`)

type Repository interface {
	UserFees(context.Context, string) (datamanager.UserFees, error)
	RefreshUserFeeTiers(context.Context) error
}

type PolicyRepository interface {
	UpdateFeePolicy(context.Context, datamanager.FeePolicyInput) error
}

type Cache interface {
	Get(context.Context, string) (datamanager.UserFees, bool, error)
	Set(context.Context, string, datamanager.UserFees, time.Duration) error
	Clear(context.Context) error
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

func (c *memoryCache) Get(_ context.Context, key string) (datamanager.UserFees, bool, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			c.mu.Lock()
			delete(c.entries, key)
			c.mu.Unlock()
		}
		return datamanager.UserFees{}, false, nil
	}
	return entry.fees, true, nil
}

func (c *memoryCache) Set(_ context.Context, key string, fees datamanager.UserFees, ttl time.Duration) error {
	c.mu.Lock()
	c.entries[key] = cacheEntry{fees: fees, expiresAt: time.Now().Add(ttl)}
	c.mu.Unlock()
	return nil
}

func (c *memoryCache) Clear(context.Context) error {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.mu.Unlock()
	return nil
}

type Service struct {
	repository Repository
	cache      Cache
	ttl        time.Duration
}

func NewService(repository Repository, cache Cache) *Service {
	return NewServiceWithTTL(repository, cache, 5*time.Minute)
}

func NewServiceWithTTL(repository Repository, cache Cache, ttl time.Duration) *Service {
	if cache == nil {
		cache = newMemoryCache()
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Service{repository: repository, cache: cache, ttl: ttl}
}

func (s *Service) GetUserFees(ctx context.Context, userID, pair string) (datamanager.UserFees, error) {
	key := userID + ":" + pair
	if fees, ok, err := s.cache.Get(ctx, key); err == nil && ok {
		return fees, nil
	}
	fees, err := s.repository.UserFees(ctx, userID)
	if err != nil {
		return datamanager.UserFees{}, err
	}
	_ = s.cache.Set(ctx, key, fees, s.ttl)
	return fees, nil
}

func (s *Service) RefreshUserFeeTiers(ctx context.Context) error {
	if err := s.repository.RefreshUserFeeTiers(ctx); err != nil {
		return err
	}
	if err := s.cache.Clear(ctx); err != nil {
		return fmt.Errorf("invalidate fee cache: %w", err)
	}
	return nil
}

func (s *Service) ConfigureTier(ctx context.Context, input datamanager.FeePolicyInput) error {
	input.MinimumVolume = strings.TrimSpace(input.MinimumVolume)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.TierLevel < 0 || input.TierLevel > 255 || input.MakerFeeBPS < -10000 || input.MakerFeeBPS > 10000 || input.TakerFeeBPS < 0 || input.TakerFeeBPS > 10000 || !decimal24Scale8.MatchString(input.MinimumVolume) || input.Reason == "" || len(input.Reason) > 1000 {
		return ErrInvalidPolicy
	}
	repository, ok := s.repository.(PolicyRepository)
	if !ok {
		return fmt.Errorf("fee policy repository is unavailable")
	}
	if err := repository.UpdateFeePolicy(ctx, input); err != nil {
		return err
	}
	if err := s.cache.Clear(ctx); err != nil {
		return fmt.Errorf("invalidate fee cache: %w", err)
	}
	return nil
}
