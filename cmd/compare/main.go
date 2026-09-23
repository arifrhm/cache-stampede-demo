package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type BenchmarkResult struct {
	Mode               string  `json:"mode"`
	TotalRequests      int     `json:"total_requests"`
	Concurrency        int     `json:"concurrency"`
	SuccessCount       int64   `json:"success_count"`
	ErrorCount         int64   `json:"error_count"`
	DurationMs         float64 `json:"duration_ms"`
	RPS                float64 `json:"rps"`
	P50Ms              float64 `json:"p50_ms"`
	P95Ms              float64 `json:"p95_ms"`
	P99Ms              float64 `json:"p99_ms"`
	MaxMs              float64 `json:"max_ms"`
	AvgMs              float64 `json:"avg_ms"`
	CacheHits          int64   `json:"cache_hits"`
	CacheMisses        int64   `json:"cache_misses"`
	DatabaseQueries    int64   `json:"database_queries"`
	MaxDBConcurrency   int64   `json:"max_db_concurrency"`
	SingleflightCalls  int64   `json:"singleflight_calls"`
	SingleflightShared int64   `json:"singleflight_shared"`
}

type ComparisonReport struct {
	Baseline               BenchmarkResult `json:"baseline"`
	Singleflight           BenchmarkResult `json:"singleflight"`
	DBQueryReductionFactor float64         `json:"db_query_reduction_factor"`
	MaxConcurrencyRatio    float64         `json:"max_concurrency_ratio"`
	LatencyP50Improvement  float64         `json:"latency_p50_improvement_ms"`
	LatencyP99Improvement  float64         `json:"latency_p99_improvement_ms"`
	SingleflightShareRatio float64         `json:"singleflight_share_ratio_percent"`
}

func main() {
	baselinePath := flag.String("baseline", "results/baseline.json", "Path to baseline JSON")
	singleflightPath := flag.String("singleflight", "results/singleflight.json", "Path to singleflight JSON")
	jsonOut := flag.String("json-out", "results/comparison.json", "Path to comparison JSON output")
	txtOut := flag.String("txt-out", "results/comparison.txt", "Path to comparison TXT output")
	flag.Parse()

	baseData, err := os.ReadFile(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading baseline result: %v\n", err)
		os.Exit(1)
	}
	var baseline BenchmarkResult
	if err := json.Unmarshal(baseData, &baseline); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing baseline JSON: %v\n", err)
		os.Exit(1)
	}

	sfData, err := os.ReadFile(*singleflightPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading singleflight result: %v\n", err)
		os.Exit(1)
	}
	var sf BenchmarkResult
	if err := json.Unmarshal(sfData, &sf); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing singleflight JSON: %v\n", err)
		os.Exit(1)
	}

	reductionFactor := 0.0
	if sf.DatabaseQueries > 0 {
		reductionFactor = float64(baseline.DatabaseQueries) / float64(sf.DatabaseQueries)
	} else if baseline.DatabaseQueries > 0 {
		reductionFactor = float64(baseline.DatabaseQueries)
	}

	concurrencyRatio := 0.0
	if sf.MaxDBConcurrency > 0 {
		concurrencyRatio = float64(baseline.MaxDBConcurrency) / float64(sf.MaxDBConcurrency)
	}

	shareRatio := 0.0
	if sf.TotalRequests > 0 {
		shareRatio = (float64(sf.SingleflightShared) / float64(sf.TotalRequests)) * 100.0
	}

	report := ComparisonReport{
		Baseline:               baseline,
		Singleflight:           sf,
		DBQueryReductionFactor: reductionFactor,
		MaxConcurrencyRatio:    concurrencyRatio,
		LatencyP50Improvement:  baseline.P50Ms - sf.P50Ms,
		LatencyP99Improvement:  baseline.P99Ms - sf.P99Ms,
		SingleflightShareRatio: shareRatio,
	}

	// Write JSON output
	if reportJSON, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = os.WriteFile(*jsonOut, reportJSON, 0644)
	}

	// Format text table
	txt := fmt.Sprintf(`======================================================================
COMPARISON SUMMARY: BASELINE vs SINGLEFLIGHT
======================================================================

Metric                  Baseline (No Coalescing)    Singleflight (Coalesced)
----------------------------------------------------------------------
Total Requests          %-27d %-24d
Cache Misses            %-27d %-24d
Database Queries        %-27d %-24d
Max DB Concurrency      %-27d %-24d
Singleflight Shared     %-27s %-24d
Duration                %-24.2f ms   %-21.2f ms
Throughput (RPS)        %-24.1f req/s %-21.1f req/s
Latency p50             %-24.2f ms   %-21.2f ms
Latency p95             %-24.2f ms   %-21.2f ms
Latency p99             %-24.2f ms   %-21.2f ms
Latency Max             %-24.2f ms   %-21.2f ms
Errors                  %-27d %-24d
----------------------------------------------------------------------

ANALYSIS:
  * Database work reduction:    %.1fx reduction in DB load
  * Database queries:           %d (baseline) -> %d (singleflight)
  * Peak DB connection demand:  %d concurrent -> %d concurrent
  * Result sharing efficiency:  %.2f%% of requests piggybacked on in-flight query

======================================================================
`,
		baseline.TotalRequests, sf.TotalRequests,
		baseline.CacheMisses, sf.CacheMisses,
		baseline.DatabaseQueries, sf.DatabaseQueries,
		baseline.MaxDBConcurrency, sf.MaxDBConcurrency,
		"N/A", sf.SingleflightShared,
		baseline.DurationMs, sf.DurationMs,
		baseline.RPS, sf.RPS,
		baseline.P50Ms, sf.P50Ms,
		baseline.P95Ms, sf.P95Ms,
		baseline.P99Ms, sf.P99Ms,
		baseline.MaxMs, sf.MaxMs,
		baseline.ErrorCount, sf.ErrorCount,
		reductionFactor,
		baseline.DatabaseQueries, sf.DatabaseQueries,
		baseline.MaxDBConcurrency, sf.MaxDBConcurrency,
		shareRatio,
	)

	_ = os.WriteFile(*txtOut, []byte(txt), 0644)
	fmt.Print(txt)
}
