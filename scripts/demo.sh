#!/usr/bin/env bash
set -euo pipefail

# Ensure sufficient file descriptors for 10,000 concurrent sockets
ulimit -n 65536 2>/dev/null || ulimit -n 10240 2>/dev/null || true

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${PROJECT_DIR}"

DEMO_MODE="${DEMO_MODE:-true}"
DEMO_DELAY="${DEMO_DELAY:-1}"
REQUESTS="${REQUESTS:-10000}"
CONCURRENCY="${CONCURRENCY:-10000}"
DB_LATENCY_MS="${DB_LATENCY_MS:-100}"
DB_MAX_CONNS="${DB_MAX_CONNS:-20}"
CACHE_TTL_SECONDS="${CACHE_TTL_SECONDS:-2}"

# Styling
BOLD="\033[1m"
GREEN="\033[32m"
CYAN="\033[36m"
YELLOW="\033[33m"
RED="\033[31m"
RESET="\033[0m"

demo_pause() {
    if [ "${DEMO_MODE}" = "true" ]; then
        sleep "${DEMO_DELAY}"
    fi
}

echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}  CACHE STAMPEDE (THUNDERING HERD) vs GO SINGLEFLIGHT DEMO           ${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo "Configuration:"
echo "  Requests:           ${REQUESTS}"
echo "  Concurrency:        ${CONCURRENCY}"
echo "  DB Latency:         ${DB_LATENCY_MS}ms"
echo "  DB Max Connections: ${DB_MAX_CONNS}"
echo "  Cache TTL:          ${CACHE_TTL_SECONDS}s"
echo "  Open Files Limit:   $(ulimit -n)"
echo ""

mkdir -p bin results

# Clean previous results
rm -f results/*.json results/*.txt

# Background process management
PIDS=()
cleanup() {
    echo ""
    echo -e "${YELLOW}Stopping background application instances...${RESET}"
    for pid in "${PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    wait 2>/dev/null || true
    echo -e "${GREEN}Cleanup completed.${RESET}"
}
trap cleanup EXIT INT TERM

echo -e "${BOLD}[1/7] Building Go binaries...${RESET}"
go build -o bin/server ./cmd/server
go build -o bin/loadtest ./cmd/loadtest
go build -o bin/compare ./cmd/compare
echo -e "${GREEN}✓ Binaries built successfully.${RESET}"
echo ""

echo -e "${BOLD}[2/7] Starting Infrastructure (Docker Compose)...${RESET}"
docker compose up -d postgres redis
./scripts/wait-for-services.sh
echo -e "${GREEN}✓ Infrastructure ready.${RESET}"
echo ""
demo_pause

echo -e "${BOLD}[3/7] Starting Primary Application Server (Port 8880)...${RESET}"
PORT=8880 \
DB_PORT=5433 \
REDIS_ADDR=localhost:6380 \
DB_MAX_CONNS=${DB_MAX_CONNS} \
DB_LATENCY_MS=${DB_LATENCY_MS} \
CACHE_TTL_SECONDS=${CACHE_TTL_SECONDS} \
./bin/server > /tmp/csd-server-8880.log 2>&1 &
SERVER_PID=$!
PIDS+=("$SERVER_PID")

# Wait for server health
retries=0
until curl -s http://localhost:8880/health | grep -q "ok"; do
    sleep 0.2
    retries=$((retries + 1))
    if [ "$retries" -ge 50 ]; then
        echo -e "${RED}Failed to connect to application server on port 8880${RESET}"
        cat /tmp/csd-server-8880.log
        exit 1
    fi
done
echo -e "${GREEN}✓ Server ready at http://localhost:8880 (Health: OK)${RESET}"
echo ""
demo_pause

# ======================================================================
# WARM CACHE VALIDATION
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EXPERIMENT 0: WARM CACHE (Baseline sanity check)${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo "Populating Redis key 'user:123' with 60s TTL for warm validation..."
docker exec csd-redis redis-cli SET "user:123" '{"id":123,"name":"Alice Wonderland","email":"alice@example.com"}' EX 60 > /dev/null
curl -s -X POST "http://localhost:8880/stats/reset" > /dev/null

echo "Sending ${REQUESTS} requests to WARM cache..."
./bin/loadtest -url=http://localhost:8880 -requests=${REQUESTS} -concurrency=${CONCURRENCY} -mode=baseline -silent=true
echo ""
demo_pause

# ======================================================================
# EXPERIMENT 1: WITHOUT SINGLEFLIGHT
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EXPERIMENT 1: WITHOUT SINGLEFLIGHT (Cache Stampede)${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo ""
echo -e "${BOLD}Architecture Flow:${RESET}"
cat << "EOF"
      WITHOUT SINGLEFLIGHT

      10,000 REQUESTS
            │
            ▼
          REDIS
            │
       CACHE MISS
            │
            ▼
        DATABASE
            │
       MANY QUERIES (Thundering Herd!)
EOF
echo ""
echo "Invalidating cache key 'user:123'..."
curl -s -X POST "http://localhost:8880/cache/invalidate/user/123" > /dev/null
curl -s -X POST "http://localhost:8880/stats/reset" > /dev/null
echo -e "${YELLOW}CACHE STATUS: COLD / MISS${RESET}"
echo ""
echo "Launching ${REQUESTS} concurrent requests without request coalescing..."
./bin/loadtest \
    -url=http://localhost:8880 \
    -requests=${REQUESTS} \
    -concurrency=${CONCURRENCY} \
    -mode=baseline \
    -out=results/baseline.json

echo ""
demo_pause

# ======================================================================
# EXPERIMENT 2: WITH SINGLEFLIGHT
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EXPERIMENT 2: WITH SINGLEFLIGHT (Request Coalescing)${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo ""
echo -e "${BOLD}Architecture Flow:${RESET}"
cat << "EOF"
       WITH SINGLEFLIGHT

      10,000 REQUESTS
            │
            ▼
          REDIS
            │
       CACHE MISS
            │
            ▼
       SINGLEFLIGHT
            │
            ▼
        DATABASE
            │
         1 QUERY
            │
            ▼
       SHARED RESULT
            │
            ▼
      10,000 CALLERS
EOF
echo ""
echo "Invalidating cache key 'user:123'..."
curl -s -X POST "http://localhost:8880/cache/invalidate/user/123" > /dev/null
curl -s -X POST "http://localhost:8880/stats/reset" > /dev/null
echo -e "${YELLOW}CACHE STATUS: COLD / MISS${RESET}"
echo ""
echo "Launching ${REQUESTS} concurrent requests WITH Go singleflight..."
./bin/loadtest \
    -url=http://localhost:8880 \
    -requests=${REQUESTS} \
    -concurrency=${CONCURRENCY} \
    -mode=singleflight \
    -out=results/singleflight.json

echo ""
demo_pause

# ======================================================================
# COMPARISON SUMMARY
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EMPIRICAL COMPARISON & REDUCTION ANALYSIS${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
./bin/compare \
    -baseline=results/baseline.json \
    -singleflight=results/singleflight.json \
    -json-out=results/comparison.json \
    -txt-out=results/comparison.txt

demo_pause

# ======================================================================
# TTL EXPIRATION DEMO
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EXPERIMENT 3: REAL-WORLD TTL EXPIRATION BURST${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo "Step 1: Populating Redis key with TTL=${CACHE_TTL_SECONDS}s..."
curl -s "http://localhost:8880/users/123?mode=baseline" > /dev/null
echo "Step 2: Key verified in Redis."
echo "Step 3: Waiting ${CACHE_TTL_SECONDS} seconds for TTL natural expiration..."
sleep "${CACHE_TTL_SECONDS}"
sleep 0.2
echo -e "${YELLOW}CACHE STATUS: EXPIRED!${RESET}"
echo "Step 4: Immediately bursting ${REQUESTS} requests on expired key with singleflight..."
curl -s -X POST "http://localhost:8880/stats/reset" > /dev/null
./bin/loadtest -url=http://localhost:8880 -requests=${REQUESTS} -concurrency=${CONCURRENCY} -mode=singleflight -silent=true
EXP_SNAP=$(curl -s http://localhost:8880/stats)
echo -e "${GREEN}Result after natural TTL expiration burst:${RESET}"
echo "${EXP_SNAP}" | grep -o '"database_queries":[0-9]*'
echo "${EXP_SNAP}" | grep -o '"singleflight_shared":[0-9]*'
echo ""
demo_pause

# ======================================================================
# MULTI-INSTANCE EXPERIMENT
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}EXPERIMENT 4: MULTI-INSTANCE TOPOLOGY (Process-Local Boundary)${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo "Starting 3 independent application instances:"
echo "  - Instance A (port 8081)"
echo "  - Instance B (port 8082)"
echo "  - Instance C (port 8083)"

for port in 8081 8082 8083; do
    PORT=${port} \
    DB_PORT=5433 \
    REDIS_ADDR=localhost:6380 \
    DB_MAX_CONNS=${DB_MAX_CONNS} \
    DB_LATENCY_MS=${DB_LATENCY_MS} \
    CACHE_TTL_SECONDS=${CACHE_TTL_SECONDS} \
    ./bin/server > /tmp/csd-server-${port}.log 2>&1 &
    PID=$!
    PIDS+=("$PID")
done

# Wait for all 3 instances
for port in 8081 8082 8083; do
    retries=0
    until curl -s "http://localhost:${port}/health" | grep -q "ok"; do
        sleep 0.1
        retries=$((retries + 1))
        if [ "$retries" -ge 50 ]; then
            echo -e "${RED}Instance on port ${port} failed to start${RESET}"
            exit 1
        fi
    done
done
echo -e "${GREEN}✓ All 3 instances healthy.${RESET}"

echo "Invalidating cache key 'user:123'..."
curl -s -X POST "http://localhost:8081/cache/invalidate/user/123" > /dev/null
for port in 8081 8082 8083; do
    curl -s -X POST "http://localhost:${port}/stats/reset" > /dev/null
done

echo "Sending ${REQUESTS} concurrent requests distributed across 3 instances..."
./bin/loadtest \
    -targets=http://localhost:8081,http://localhost:8082,http://localhost:8083 \
    -requests=${REQUESTS} \
    -concurrency=${CONCURRENCY} \
    -mode=singleflight \
    -out=results/multi-instance.json

echo ""
echo -e "${BOLD}Instance Breakdown:${RESET}"
for port in 8081 8082 8083; do
    STAT=$(curl -s "http://localhost:${port}/stats")
    QUERIES=$(echo "$STAT" | grep -o '"database_queries":[0-9]*' | cut -d: -f2)
    SHARED=$(echo "$STAT" | grep -o '"singleflight_shared":[0-9]*' | cut -d: -f2)
    echo "  Port ${port} -> DB Queries: ${QUERIES}, Singleflight Shared: ${SHARED}"
done

echo ""
echo -e "${YELLOW}Takeaway:${RESET}"
echo "  singleflight is process-local. Each process independently executes its own"
echo "  coalesced DB query. To coordinate across multiple instances, combine"
echo "  singleflight with distributed locking (e.g. Redis lock) or probabilistic early expiration (SWR)."
echo ""
demo_pause

# ======================================================================
# CONCLUSION
# ======================================================================
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
echo -e "${BOLD}${CYAN}SUMMARY & LESSONS${RESET}"
echo -e "${BOLD}${CYAN}======================================================================${RESET}"
cat << "EOF"
      CACHE
        │
        ▼ Reduces normal database traffic
  
  SINGLEFLIGHT
        │
        ▼ Deduplicates concurrent identical work within the process
  
  DISTRIBUTED LOCK / SWR / JITTER
        │
        ▼ Additional techniques for cross-instance protection & stagger
EOF
echo ""
echo -e "${GREEN}Demo finished successfully.${RESET}"
echo "Results saved in:"
echo "  - results/baseline.json"
echo "  - results/singleflight.json"
echo "  - results/comparison.json"
echo "  - results/comparison.txt"
echo "  - results/multi-instance.json"
