package metrics

import (
	"sync/atomic"
)

type Snapshot struct {
	TotalRequests        int64 `json:"total_requests"`
	CacheHits            int64 `json:"cache_hits"`
	CacheMisses          int64 `json:"cache_misses"`
	DatabaseQueries      int64 `json:"database_queries"`
	CurrentDBConcurrency int64 `json:"current_db_concurrency"`
	MaxDBConcurrency     int64 `json:"max_db_concurrency"`
	SingleflightCalls    int64 `json:"singleflight_calls"`
	SingleflightShared   int64 `json:"singleflight_shared"`
	RedisLockAcquired    int64 `json:"redis_lock_acquired"`
	RedisLockContended   int64 `json:"redis_lock_contended"`
	Errors               int64 `json:"errors"`
}

type Tracker struct {
	totalRequests        atomic.Int64
	cacheHits            atomic.Int64
	cacheMisses          atomic.Int64
	databaseQueries      atomic.Int64
	currentDBConcurrency atomic.Int64
	maxDBConcurrency     atomic.Int64
	singleflightCalls    atomic.Int64
	singleflightShared   atomic.Int64
	redisLockAcquired    atomic.Int64
	redisLockContended   atomic.Int64
	errors               atomic.Int64
}

func NewTracker() *Tracker {
	return &Tracker{}
}

func (t *Tracker) IncRequests()           { t.totalRequests.Add(1) }
func (t *Tracker) IncCacheHits()          { t.cacheHits.Add(1) }
func (t *Tracker) IncCacheMisses()        { t.cacheMisses.Add(1) }
func (t *Tracker) IncDatabaseQueries()    { t.databaseQueries.Add(1) }
func (t *Tracker) IncSingleflightCalls()  { t.singleflightCalls.Add(1) }
func (t *Tracker) IncSingleflightShared() { t.singleflightShared.Add(1) }
func (t *Tracker) IncRedisLockAcquired()  { t.redisLockAcquired.Add(1) }
func (t *Tracker) IncRedisLockContended() { t.redisLockContended.Add(1) }
func (t *Tracker) IncErrors()             { t.errors.Add(1) }

func (t *Tracker) SetDBConcurrency(current, max int64) {
	t.currentDBConcurrency.Store(current)
	for {
		curMax := t.maxDBConcurrency.Load()
		if max <= curMax {
			break
		}
		if t.maxDBConcurrency.CompareAndSwap(curMax, max) {
			break
		}
	}
}

func (t *Tracker) Snapshot() Snapshot {
	return Snapshot{
		TotalRequests:        t.totalRequests.Load(),
		CacheHits:            t.cacheHits.Load(),
		CacheMisses:          t.cacheMisses.Load(),
		DatabaseQueries:      t.databaseQueries.Load(),
		CurrentDBConcurrency: t.currentDBConcurrency.Load(),
		MaxDBConcurrency:     t.maxDBConcurrency.Load(),
		SingleflightCalls:    t.singleflightCalls.Load(),
		SingleflightShared:   t.singleflightShared.Load(),
		RedisLockAcquired:    t.redisLockAcquired.Load(),
		RedisLockContended:   t.redisLockContended.Load(),
		Errors:               t.errors.Load(),
	}
}

func (t *Tracker) Reset() {
	t.totalRequests.Store(0)
	t.cacheHits.Store(0)
	t.cacheMisses.Store(0)
	t.databaseQueries.Store(0)
	t.currentDBConcurrency.Store(0)
	t.maxDBConcurrency.Store(0)
	t.singleflightCalls.Store(0)
	t.singleflightShared.Store(0)
	t.redisLockAcquired.Store(0)
	t.redisLockContended.Store(0)
	t.errors.Store(0)
}
