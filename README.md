# Cache Stampede (Thundering Herd) vs Go Singleflight

Empirical Proof-of-Concept and Demonstration comparing **Unmitigated Cache Misses** (Cache Stampede) against **Request Coalescing** via Go's `golang.org/x/sync/singleflight`.

---

## 📌 Problem: The Cache Stampede

When a highly popular cache key (e.g. `user:123`) expires or is invalidated, thousands of concurrent requests can simultaneously experience a **cache miss**.

```
WITHOUT SINGLEFLIGHT (Thundering Herd)

10,000 Concurrent Requests
         │
         ▼
     Redis GET
         │
     CACHE MISS (Key expired)
         │
         ▼
   10,000 Database Queries (SELECT ... WHERE id = $1)
         │
   [DATABASE CONNECTION POOL EXHAUSTION & HIGH LATENCY]
```

Without synchronization, **every single concurrent request** hits the database at the same time, leading to connection pool starvation, latency spikes, and potential database collapse.

---

## ⚡ Solution: Request Coalescing with `singleflight.Group`

Go's `singleflight.Group` suppresses duplicate concurrent function calls for the same key. If 10,000 requests arrive while a database query is already in-flight for `user:123`, only **one** request executes the database query. The other 9,999 callers wait and share the single result.

```
WITH SINGLEFLIGHT (Request Coalescing)

10,000 Concurrent Requests
         │
         ▼
     Redis GET
         │
     CACHE MISS
         │
         ▼
    singleflight.Do("user:123")
         │
         ├───► Double Check Redis (still miss?)
         │        │
         │        ▼
         │   1 Database Query
         │        │
         │        ▼
         │   Populate Redis
         │
         ▼
    1 Result Shared with all 10,000 Callers
```

### Critical Implementation Detail: The Double-Check Pattern
Inside `singleflight.Do`, we always **double-check** Redis. If another goroutine populated Redis immediately before our callback ran, we return the cached value directly without querying the database.

---

## 🧪 Architecture & Project Structure

```
cache-stampede-demo/
├── cmd/
│   ├── server/             # Go HTTP server with Redis & Postgres connection pool
│   ├── loadtest/           # Barrier-burst concurrent load generator (10,000 requests)
│   └── compare/            # Empirical results comparator and summary formatter
├── internal/
│   ├── cache/              # Redis client wrapper with distributed lock support
│   ├── database/           # Postgres repository, connection pool, artificial latency
│   ├── handler/            # HTTP handlers (/users/:id, /stats, /health, /dashboard)
│   ├── service/            # Business logic (Baseline, Singleflight, Redis Lock)
│   └── metrics/            # Atomic metrics tracker (queries, concurrency, latency)
├── scripts/
│   ├── demo.sh             # Fully automated end-to-end demonstration
│   ├── record-demo.sh      # asciinema & MP4 recording helper
│   ├── wait-for-services.sh# PostgreSQL & Redis readiness probe
│   └── init-db.sql         # Database schema & seed data
├── results/                # Actual empirical JSON & TXT benchmark outputs
├── docker-compose.yml      # Isolated PostgreSQL (5433) & Redis (6380)
├── Makefile                # make build, make test, make up, make demo, make record
├── go.mod
└── README.md
```

---

## 🚀 Quick Start

### 1. Prerequisites
- Docker & Docker Compose
- Go 1.22+
- macOS / Linux terminal with `ulimit -n` >= 10240

### 2. Run the Full Automated Demo
The entire proof can be executed with a single command:

```bash
make demo
```

This will automatically:
1. Compile the server, load tester, and comparison binaries.
2. Spin up dedicated PostgreSQL (`port 5433`) and Redis (`port 6380`) containers.
3. Seed the `users` table with user `123`.
4. Run **Experiment 0**: Warm cache validation (DB queries = 0).
5. Run **Experiment 1**: Baseline without singleflight (10,000 requests on cold cache).
6. Run **Experiment 2**: With singleflight (10,000 requests on cold cache).
7. Run **Experiment 3**: Real-world TTL expiration burst.
8. Run **Experiment 4**: Multi-instance demonstration (3 application processes).
9. Output formatted comparison metrics to `results/comparison.txt` and `results/comparison.json`.

---

## 📊 Live Metrics Dashboard

While the server is running, you can visit the real-time browser dashboard:

```
http://localhost:8880/dashboard
```

---

## 🎥 Demonstration Video & Terminal Recording

The repository includes the authentic recorded terminal session and MP4 video:

- 🎬 **Terminal Recording Video (MP4)**: [`results/cache-stampede-demo.mp4`](results/cache-stampede-demo.mp4) (Rendered from real terminal session using `asciinema` + `agg` + `ffmpeg`)
- 📜 **Asciicast File**: [`results/cache-stampede-demo.cast`](results/cache-stampede-demo.cast) (Playable with `asciinema play results/cache-stampede-demo.cast`)

### Re-recording on your machine:
```bash
make record
```
This automatically captures the live terminal output into `.cast` and exports `.mp4`.

### Option B: Via OBS Studio
- **Resolution**: 1920x1080 (16:9)
- **Framerate**: 30 FPS
- **Terminal Font Size**: 18–24pt (Monaco, Menlo, or JetBrains Mono)
- **Terminal Window**: Standard 80x24 to 120x35 layout
- **Command to record**:
  ```bash
  make demo
  ```

---

## 🧠 Core Engineering Principles

| Technique | Scope | Purpose |
| :--- | :--- | :--- |
| **Normal Caching** | Central (Redis) | Drastically reduces steady-state database read traffic. |
| **Singleflight** | **Process-Local** | Prevents concurrent duplicate queries for the **same key** within a single application process. |
| **Distributed Lock** | Cross-Process (Redis) | Coordinates exclusive cache rebuilds across multiple distinct application instances. |
| **TTL Jitter** | Key Generation | Adds random variation to expiration times so multiple keys don't expire simultaneously. |
| **Stale-While-Revalidate (SWR)** | Background | Serves stale data while asynchronously refreshing the cache in the background. |

> **Key takeaway:** `singleflight` is **process-local**. If you run 3 application instances behind a load balancer, each instance will execute at most 1 database query during a stampede (3 total database queries instead of 10,000). To achieve cross-instance coalescing, use a distributed lock or SWR.
