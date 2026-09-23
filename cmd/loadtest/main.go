package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cache-stampede-demo/internal/metrics"
)

type LoadTestResult struct {
	Mode               string        `json:"mode"`
	TotalRequests      int           `json:"total_requests"`
	Concurrency        int           `json:"concurrency"`
	SuccessCount       int64         `json:"success_count"`
	ErrorCount         int64         `json:"error_count"`
	Duration           time.Duration `json:"duration"`
	DurationMs         float64       `json:"duration_ms"`
	RPS                float64       `json:"rps"`
	P50Ms              float64       `json:"p50_ms"`
	P95Ms              float64       `json:"p95_ms"`
	P99Ms              float64       `json:"p99_ms"`
	MaxMs              float64       `json:"max_ms"`
	AvgMs              float64       `json:"avg_ms"`
	CacheHits          int64         `json:"cache_hits"`
	CacheMisses        int64         `json:"cache_misses"`
	DatabaseQueries    int64         `json:"database_queries"`
	MaxDBConcurrency   int64         `json:"max_db_concurrency"`
	SingleflightCalls  int64         `json:"singleflight_calls"`
	SingleflightShared int64         `json:"singleflight_shared"`
	InstanceBreakdown  []InstanceStat `json:"instance_breakdown,omitempty"`
}

type InstanceStat struct {
	URL             string `json:"url"`
	Requests        int64  `json:"requests"`
	DatabaseQueries int64  `json:"database_queries"`
	CacheMisses     int64  `json:"cache_misses"`
}

func fetchStats(client *http.Client, baseURL string) (metrics.Snapshot, error) {
	resp, err := client.Get(baseURL + "/stats")
	if err != nil {
		return metrics.Snapshot{}, err
	}
	defer resp.Body.Close()

	var snap metrics.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return metrics.Snapshot{}, err
	}
	return snap, nil
}

func main() {
	var (
		baseURL     = flag.String("url", "http://localhost:8880", "Base URL of target service")
		targetURLs  = flag.String("targets", "", "Comma-separated target URLs for multi-instance testing")
		requests    = flag.Int("requests", 10000, "Total number of requests to send")
		concurrency = flag.Int("concurrency", 10000, "Number of concurrent workers")
		userID      = flag.Int64("user-id", 123, "User ID to query")
		mode        = flag.String("mode", "baseline", "Mode to test: baseline, singleflight, or redis-lock")
		outPath     = flag.String("out", "", "Output JSON path for results")
		silent      = flag.Bool("silent", false, "Suppress per-worker logging")
	)
	flag.Parse()

	// Handle environment overrides if flags weren't explicitly passed
	if v := os.Getenv("REQUESTS"); v != "" && *requests == 10000 {
		fmt.Sscanf(v, "%d", requests)
	}
	if v := os.Getenv("CONCURRENCY"); v != "" && *concurrency == 10000 {
		fmt.Sscanf(v, "%d", concurrency)
	}
	if v := os.Getenv("MODE"); v != "" && *mode == "baseline" {
		*mode = v
	}
	if v := os.Getenv("URL"); v != "" && *baseURL == "http://localhost:8880" {
		*baseURL = v
	}

	urls := []string{*baseURL}
	if *targetURLs != "" {
		urls = strings.Split(*targetURLs, ",")
		for i := range urls {
			urls[i] = strings.TrimSpace(urls[i])
		}
	}

	transport := &http.Transport{
		MaxIdleConns:        25000,
		MaxIdleConnsPerHost: 25000,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}

	// 1. Fetch initial stats from all targets
	type initialStat struct {
		url  string
		snap metrics.Snapshot
	}
	initialStats := make([]initialStat, len(urls))
	for i, u := range urls {
		s, err := fetchStats(httpClient, u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch stats from %s: %v\n", u, err)
		}
		initialStats[i] = initialStat{url: u, snap: s}
	}

	if !*silent {
		fmt.Printf("Preparing %d concurrent workers targeting %s (mode: %s, user_id: %d)...\n",
			*concurrency, strings.Join(urls, ", "), *mode, *userID)
	}

	// Synchronization barriers
	var readyWg sync.WaitGroup
	readyWg.Add(*requests)
	startGate := make(chan struct{})

	latencies := make([]float64, *requests)
	var successCount atomic.Int64
	var errorCount atomic.Int64
	reqCounterPerTarget := make([]atomic.Int64, len(urls))

	var doneWg sync.WaitGroup
	doneWg.Add(*requests)

	// Spin up workers
	for i := 0; i < *requests; i++ {
		idx := i
		targetURL := urls[idx%len(urls)]
		targetIdx := idx % len(urls)

		go func() {
			defer doneWg.Done()

			// Prepare request
			reqURL := fmt.Sprintf("%s/users/%d?mode=%s", targetURL, *userID, *mode)
			req, err := http.NewRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				errorCount.Add(1)
				readyWg.Done()
				return
			}
			req.Header.Set("X-Mode", *mode)

			// Signal ready
			readyWg.Done()

			// Wait at barrier gate
			<-startGate

			reqCounterPerTarget[targetIdx].Add(1)

			// Execute request with retry for transient burst socket connection drops
			start := time.Now()
			var resp *http.Response
			for attempt := 0; attempt < 3; attempt++ {
				resp, err = httpClient.Do(req)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			duration := time.Since(start).Seconds() * 1000.0 // in ms

			latencies[idx] = duration

			if err != nil {
				errorCount.Add(1)
				return
			}

			// Read and discard body
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				successCount.Add(1)
			} else {
				errorCount.Add(1)
			}
		}()
	}

	// Wait for all goroutines to reach the starting gate
	readyWg.Wait()
	if !*silent {
		fmt.Println("All workers ready. Releasing start gate barrier for simultaneous burst...")
	}

	benchmarkStart := time.Now()
	// Open the gate!
	close(startGate)

	// Wait for all requests to finish
	doneWg.Wait()
	benchmarkDuration := time.Since(benchmarkStart)

	// 2. Fetch final stats from all targets
	var totalDBQueries int64
	var totalCacheMisses int64
	var totalCacheHits int64
	var maxDBConcurrency int64
	var totalSingleflightCalls int64
	var totalSingleflightShared int64
	var breakdown []InstanceStat

	for i, u := range urls {
		finalSnap, err := fetchStats(httpClient, u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch final stats from %s: %v\n", u, err)
		}
		initSnap := initialStats[i].snap

		dbQueriesDelta := finalSnap.DatabaseQueries - initSnap.DatabaseQueries
		if dbQueriesDelta < 0 {
			dbQueriesDelta = finalSnap.DatabaseQueries
		}
		cacheMissesDelta := finalSnap.CacheMisses - initSnap.CacheMisses
		if cacheMissesDelta < 0 {
			cacheMissesDelta = finalSnap.CacheMisses
		}
		cacheHitsDelta := finalSnap.CacheHits - initSnap.CacheHits
		if cacheHitsDelta < 0 {
			cacheHitsDelta = finalSnap.CacheHits
		}
		sfCallsDelta := finalSnap.SingleflightCalls - initSnap.SingleflightCalls
		sfSharedDelta := finalSnap.SingleflightShared - initSnap.SingleflightShared

		totalDBQueries += dbQueriesDelta
		totalCacheMisses += cacheMissesDelta
		totalCacheHits += cacheHitsDelta
		totalSingleflightCalls += sfCallsDelta
		totalSingleflightShared += sfSharedDelta

		if finalSnap.MaxDBConcurrency > maxDBConcurrency {
			maxDBConcurrency = finalSnap.MaxDBConcurrency
		}

		breakdown = append(breakdown, InstanceStat{
			URL:             u,
			Requests:        reqCounterPerTarget[i].Load(),
			DatabaseQueries: dbQueriesDelta,
			CacheMisses:     cacheMissesDelta,
		})
	}

	// Calculate latency percentiles
	sort.Float64s(latencies)
	var sum float64
	for _, l := range latencies {
		sum += l
	}
	avgMs := 0.0
	if len(latencies) > 0 {
		avgMs = sum / float64(len(latencies))
	}

	p50 := percentile(latencies, 50)
	p95 := percentile(latencies, 95)
	p99 := percentile(latencies, 99)
	maxLatency := 0.0
	if len(latencies) > 0 {
		maxLatency = latencies[len(latencies)-1]
	}

	durationSec := benchmarkDuration.Seconds()
	rps := 0.0
	if durationSec > 0 {
		rps = float64(*requests) / durationSec
	}

	result := LoadTestResult{
		Mode:               *mode,
		TotalRequests:      *requests,
		Concurrency:        *concurrency,
		SuccessCount:       successCount.Load(),
		ErrorCount:         errorCount.Load(),
		Duration:           benchmarkDuration,
		DurationMs:         benchmarkDuration.Seconds() * 1000.0,
		RPS:                rps,
		P50Ms:              p50,
		P95Ms:              p95,
		P99Ms:              p99,
		MaxMs:              maxLatency,
		AvgMs:              avgMs,
		CacheHits:          totalCacheHits,
		CacheMisses:        totalCacheMisses,
		DatabaseQueries:    totalDBQueries,
		MaxDBConcurrency:   maxDBConcurrency,
		SingleflightCalls:  totalSingleflightCalls,
		SingleflightShared: totalSingleflightShared,
	}

	if len(breakdown) > 1 {
		result.InstanceBreakdown = breakdown
	}

	// Save to JSON if requested
	if *outPath != "" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling result: %v\n", err)
		} else {
			if err := os.WriteFile(*outPath, data, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
			}
		}
	}

	// Terminal output
	fmt.Printf("\n--- Benchmark Results [%s] ---\n", strings.ToUpper(*mode))
	fmt.Printf("Total Requests:     %d\n", result.TotalRequests)
	fmt.Printf("Success:            %d\n", result.SuccessCount)
	fmt.Printf("Errors:             %d\n", result.ErrorCount)
	fmt.Printf("Duration:           %.2f ms (%.1f req/s)\n", result.DurationMs, result.RPS)
	fmt.Printf("Cache Misses:       %d\n", result.CacheMisses)
	fmt.Printf("Database Queries:   %d\n", result.DatabaseQueries)
	fmt.Printf("Max DB Concurrency: %d\n", result.MaxDBConcurrency)
	if *mode == "singleflight" {
		fmt.Printf("Singleflight Calls:  %d\n", result.SingleflightCalls)
		fmt.Printf("Singleflight Shared: %d\n", result.SingleflightShared)
	}
	fmt.Printf("Latency (p50 / p95 / p99 / max): %.2fms / %.2fms / %.2fms / %.2fms\n",
		result.P50Ms, result.P95Ms, result.P99Ms, result.MaxMs)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*(p/100.0))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
