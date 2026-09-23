package metrics_test

import (
	"sync"
	"testing"

	"cache-stampede-demo/internal/metrics"
)

func TestTrackerConcurrentOperations(t *testing.T) {
	tracker := metrics.NewTracker()

	const workers = 50
	const iterations = 100
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.IncRequests()
				tracker.IncCacheHits()
				tracker.IncCacheMisses()
				tracker.IncDatabaseQueries()
				tracker.IncSingleflightCalls()
				tracker.IncSingleflightShared()
				tracker.SetDBConcurrency(int64(workerID), int64(workerID+1))
			}
		}(i)
	}

	wg.Wait()

	snap := tracker.Snapshot()
	expected := int64(workers * iterations)
	if snap.TotalRequests != expected {
		t.Errorf("expected %d requests, got %d", expected, snap.TotalRequests)
	}
	if snap.CacheHits != expected {
		t.Errorf("expected %d hits, got %d", expected, snap.CacheHits)
	}
	if snap.CacheMisses != expected {
		t.Errorf("expected %d misses, got %d", expected, snap.CacheMisses)
	}
	if snap.DatabaseQueries != expected {
		t.Errorf("expected %d DB queries, got %d", expected, snap.DatabaseQueries)
	}

	tracker.Reset()
	snapReset := tracker.Snapshot()
	if snapReset.TotalRequests != 0 || snapReset.DatabaseQueries != 0 {
		t.Errorf("expected reset to clear counters, got %+v", snapReset)
	}
}
