package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cache-stampede-demo/internal/cache"
	"cache-stampede-demo/internal/database"
	"cache-stampede-demo/internal/metrics"
	"cache-stampede-demo/internal/service"
)

// MockUserRepository simulates a database with atomic tracking
type MockUserRepository struct {
	mu           sync.RWMutex
	users        map[int64]*database.User
	queryCount   atomic.Int64
	latency      time.Duration
	failOnUserID int64
}

func NewMockRepo() *MockUserRepository {
	return &MockUserRepository{
		users: map[int64]*database.User{
			123: {ID: 123, Name: "Alice Wonderland", Email: "alice@example.com"},
			456: {ID: 456, Name: "Bob Builder", Email: "bob@example.com"},
		},
	}
}

func (m *MockUserRepository) GetUser(ctx context.Context, id int64) (*database.User, error) {
	m.queryCount.Add(1)

	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if m.failOnUserID == id {
		return nil, errors.New("simulated database failure")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return &database.User{ID: u.ID, Name: u.Name, Email: u.Email}, nil
}

func (m *MockUserRepository) GetMetrics() (int64, int64, int64) {
	return m.queryCount.Load(), 0, 0
}

func (m *MockUserRepository) ResetMetrics() {
	m.queryCount.Store(0)
}

func (m *MockUserRepository) Close() error {
	return nil
}

// MockCacheClient simulates in-memory Redis
type MockCacheClient struct {
	mu           sync.RWMutex
	store        map[int64]*database.User
	locks        map[string]bool
	failGet      bool
	failSet      bool
}

func NewMockCache() *MockCacheClient {
	return &MockCacheClient{
		store: make(map[int64]*database.User),
		locks: make(map[string]bool),
	}
}

func (m *MockCacheClient) GetUser(ctx context.Context, id int64) (*database.User, error) {
	if m.failGet {
		return nil, errors.New("redis connection error")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.store[id]
	if !ok {
		return nil, cache.ErrCacheMiss
	}
	return &database.User{ID: u.ID, Name: u.Name, Email: u.Email}, nil
}

func (m *MockCacheClient) SetUser(ctx context.Context, user *database.User, ttl time.Duration) error {
	if m.failSet {
		return errors.New("redis write error")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[user.ID] = &database.User{ID: user.ID, Name: user.Name, Email: user.Email}
	return nil
}

func (m *MockCacheClient) InvalidateUser(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, id)
	return nil
}

func (m *MockCacheClient) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[key] {
		return false, nil
	}
	m.locks[key] = true
	return true, nil
}

func (m *MockCacheClient) ReleaseLock(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, key)
	return nil
}

func (m *MockCacheClient) Close() error {
	return nil
}

// TestWithoutSingleflight_ConcurrentMiss verifies that without singleflight,
// N concurrent requests to a cold cache cause N database queries.
func TestWithoutSingleflight_ConcurrentMiss(t *testing.T) {
	repo := NewMockRepo()
	repo.latency = 50 * time.Millisecond
	c := NewMockCache()
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			u, err := svc.GetUserWithoutSingleflight(context.Background(), 123)
			if err != nil || u == nil {
				t.Errorf("expected success, got err: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	queries := repo.queryCount.Load()
	if queries != int64(n) {
		t.Fatalf("expected exactly %d DB queries without singleflight, got %d", n, queries)
	}
}

// TestWithSingleflight_Deduplication verifies that with singleflight,
// N concurrent requests to the same cold key coalesce into approximately 1 DB query.
func TestWithSingleflight_Deduplication(t *testing.T) {
	repo := NewMockRepo()
	repo.latency = 50 * time.Millisecond
	c := NewMockCache()
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	const n = 100
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			u, err := svc.GetUserWithSingleflight(context.Background(), 123)
			if err != nil || u == nil {
				t.Errorf("expected user, got err: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()

	queries := repo.queryCount.Load()
	if queries != 1 {
		t.Fatalf("expected exactly 1 DB query with singleflight, got %d", queries)
	}

	snap := tracker.Snapshot()
	if snap.SingleflightCalls == 0 {
		t.Errorf("expected singleflight calls > 0, got %d", snap.SingleflightCalls)
	}
	if snap.SingleflightShared == 0 {
		t.Errorf("expected singleflight shared > 0, got %d", snap.SingleflightShared)
	}
}

// TestSingleflight_DifferentKeysAreIndependent verifies that calls for different keys
// execute independently.
func TestSingleflight_DifferentKeysAreIndependent(t *testing.T) {
	repo := NewMockRepo()
	repo.latency = 30 * time.Millisecond
	c := NewMockCache()
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	var wg sync.WaitGroup
	start := make(chan struct{})

	keys := []int64{123, 456}
	for _, id := range keys {
		for i := 0; i < 20; i++ {
			userID := id
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				u, err := svc.GetUserWithSingleflight(context.Background(), userID)
				if err != nil || u == nil {
					t.Errorf("failed fetching user %d: %v", userID, err)
				}
			}()
		}
	}

	close(start)
	wg.Wait()

	queries := repo.queryCount.Load()
	if queries != 2 {
		t.Fatalf("expected exactly 2 DB queries for 2 distinct keys, got %d", queries)
	}
}

// TestDatabaseErrorPropagates verifies that DB errors are propagated to all coalesced callers.
func TestDatabaseErrorPropagates(t *testing.T) {
	repo := NewMockRepo()
	repo.failOnUserID = 999
	repo.latency = 20 * time.Millisecond
	c := NewMockCache()
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	const n = 10
	var wg sync.WaitGroup
	errCount := atomic.Int64{}
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.GetUserWithSingleflight(context.Background(), 999)
			if err != nil {
				errCount.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if errCount.Load() != int64(n) {
		t.Fatalf("expected all %d callers to receive DB error, got %d errors", n, errCount.Load())
	}
}

// TestContextCancellation verifies that cancelled contexts do not cause deadlocks.
func TestContextCancellation(t *testing.T) {
	repo := NewMockRepo()
	repo.latency = 200 * time.Millisecond
	c := NewMockCache()
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := svc.GetUserWithoutSingleflight(ctx, 123)
	if err == nil {
		t.Fatal("expected context timeout error, got nil")
	}
}

// TestCacheSetFailureHandled verifies that if cache SET fails, user is still returned.
func TestCacheSetFailureHandled(t *testing.T) {
	repo := NewMockRepo()
	c := NewMockCache()
	c.failSet = true // Redis write fails
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	u, err := svc.GetUserWithSingleflight(context.Background(), 123)
	if err != nil {
		t.Fatalf("expected user to succeed despite cache set failure, got: %v", err)
	}
	if u == nil || u.ID != 123 {
		t.Fatalf("unexpected user: %+v", u)
	}
}

// TestRedisErrorHandled verifies that if Redis GET fails, fallback to DB works.
func TestRedisErrorHandled(t *testing.T) {
	repo := NewMockRepo()
	c := NewMockCache()
	c.failGet = true // Redis read error
	tracker := metrics.NewTracker()
	svc := service.NewUserService(repo, c, tracker, 2*time.Second, 0)

	u, err := svc.GetUserWithSingleflight(context.Background(), 123)
	if err != nil {
		t.Fatalf("expected DB fallback on Redis error, got: %v", err)
	}
	if u == nil || u.ID != 123 {
		t.Fatalf("unexpected user: %+v", u)
	}
}
