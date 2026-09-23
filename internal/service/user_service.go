package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"cache-stampede-demo/internal/cache"
	"cache-stampede-demo/internal/database"
	"cache-stampede-demo/internal/metrics"
	"golang.org/x/sync/singleflight"
)

type UserService struct {
	repo       database.UserRepository
	cache      cache.CacheClient
	metrics    *metrics.Tracker
	sfGroup    singleflight.Group
	cacheTTL   time.Duration
	jitterMax  time.Duration
}

func NewUserService(repo database.UserRepository, c cache.CacheClient, m *metrics.Tracker, ttl time.Duration, jitter time.Duration) *UserService {
	return &UserService{
		repo:      repo,
		cache:     c,
		metrics:   m,
		cacheTTL:  ttl,
		jitterMax: jitter,
	}
}

func (s *UserService) calculateTTL() time.Duration {
	if s.jitterMax <= 0 {
		return s.cacheTTL
	}
	jitter := time.Duration(rand.Int63n(int64(s.jitterMax)))
	return s.cacheTTL + jitter
}

// GetUserWithoutSingleflight exhibits cache stampede / thundering herd when cache expires
func (s *UserService) GetUserWithoutSingleflight(ctx context.Context, id int64) (*database.User, error) {
	s.metrics.IncRequests()

	// 1. Try cache
	u, err := s.cache.GetUser(ctx, id)
	if err == nil && u != nil {
		s.metrics.IncCacheHits()
		return u, nil
	}
	s.metrics.IncCacheMisses()

	// 2. Query Database directly (no coalescing / deduplication)
	s.metrics.IncDatabaseQueries()
	user, err := s.repo.GetUser(ctx, id)
	if err != nil {
		s.metrics.IncErrors()
		return nil, fmt.Errorf("database query failed: %w", err)
	}

	// 3. Populate cache
	_ = s.cache.SetUser(ctx, user, s.calculateTTL())

	return user, nil
}

// GetUserWithSingleflight coalesces concurrent duplicate calls into a single in-flight call
func (s *UserService) GetUserWithSingleflight(ctx context.Context, id int64) (*database.User, error) {
	s.metrics.IncRequests()

	// 1. Initial Cache Check
	u, err := s.cache.GetUser(ctx, id)
	if err == nil && u != nil {
		s.metrics.IncCacheHits()
		return u, nil
	}
	s.metrics.IncCacheMisses()

	// 2. Coalesce with singleflight
	key := fmt.Sprintf("user:%d", id)
	s.metrics.IncSingleflightCalls()

	val, err, shared := s.sfGroup.Do(key, func() (interface{}, error) {
		// CRITICAL: Double-check cache inside singleflight callback.
		// A concurrent caller might have finished and populated the cache.
		cachedUser, cacheErr := s.cache.GetUser(ctx, id)
		if cacheErr == nil && cachedUser != nil {
			return cachedUser, nil
		}

		// Cache still empty: perform expensive DB query
		s.metrics.IncDatabaseQueries()
		dbUser, dbErr := s.repo.GetUser(ctx, id)
		if dbErr != nil {
			return nil, dbErr
		}

		// Store in cache
		_ = s.cache.SetUser(ctx, dbUser, s.calculateTTL())

		return dbUser, nil
	})

	if shared {
		s.metrics.IncSingleflightShared()
	}

	if err != nil {
		s.metrics.IncErrors()
		return nil, err
	}

	user, ok := val.(*database.User)
	if !ok {
		s.metrics.IncErrors()
		return nil, errors.New("type assertion to *User failed")
	}

	return user, nil
}

// GetUserWithRedisLock uses distributed locking across instances/processes
func (s *UserService) GetUserWithRedisLock(ctx context.Context, id int64) (*database.User, error) {
	s.metrics.IncRequests()

	// 1. Initial cache check
	u, err := s.cache.GetUser(ctx, id)
	if err == nil && u != nil {
		s.metrics.IncCacheHits()
		return u, nil
	}
	s.metrics.IncCacheMisses()

	lockKey := fmt.Sprintf("user:%d", id)
	lockTTL := 5 * time.Second

	// Try acquiring lock with polling loop
	startTime := time.Now()
	timeout := 10 * time.Second

	for {
		acquired, lockErr := s.cache.AcquireLock(ctx, lockKey, lockTTL)
		if lockErr == nil && acquired {
			s.metrics.IncRedisLockAcquired()
			defer func() {
				_ = s.cache.ReleaseLock(ctx, lockKey)
			}()

			// Double-check cache now that lock is held
			cached, cErr := s.cache.GetUser(ctx, id)
			if cErr == nil && cached != nil {
				return cached, nil
			}

			// Query DB
			s.metrics.IncDatabaseQueries()
			dbUser, dbErr := s.repo.GetUser(ctx, id)
			if dbErr != nil {
				s.metrics.IncErrors()
				return nil, dbErr
			}

			_ = s.cache.SetUser(ctx, dbUser, s.calculateTTL())
			return dbUser, nil
		}

		s.metrics.IncRedisLockContended()

		// If failed to acquire, poll cache while waiting
		select {
		case <-ctx.Done():
			s.metrics.IncErrors()
			return nil, ctx.Err()
		case <-time.After(15 * time.Millisecond):
			// Check if cache has been populated by the lock holder
			cached, cErr := s.cache.GetUser(ctx, id)
			if cErr == nil && cached != nil {
				return cached, nil
			}
		}

		if time.Since(startTime) > timeout {
			s.metrics.IncErrors()
			return nil, errors.New("timeout waiting for distributed lock")
		}
	}
}

func (s *UserService) InvalidateUser(ctx context.Context, id int64) error {
	return s.cache.InvalidateUser(ctx, id)
}
