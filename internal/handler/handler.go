package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cache-stampede-demo/internal/database"
	"cache-stampede-demo/internal/metrics"
	"cache-stampede-demo/internal/service"
)

type Handler struct {
	svc         *service.UserService
	repo        database.UserRepository
	tracker     *metrics.Tracker
	defaultMode string
}

func NewHandler(svc *service.UserService, repo database.UserRepository, tracker *metrics.Tracker, defaultMode string) *Handler {
	if defaultMode == "" {
		defaultMode = "baseline"
	}
	return &Handler{
		svc:         svc,
		repo:        repo,
		tracker:     tracker,
		defaultMode: defaultMode,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.Health)
	mux.HandleFunc("/stats", h.Stats)
	mux.HandleFunc("/stats/reset", h.ResetStats)
	mux.HandleFunc("/cache/invalidate/user/", h.InvalidateCache)
	mux.HandleFunc("/users/", h.GetUser)
	mux.HandleFunc("/dashboard", h.Dashboard)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	queries, maxConcurrency, curConcurrency := h.repo.GetMetrics()
	h.tracker.SetDBConcurrency(curConcurrency, maxConcurrency)

	snap := h.tracker.Snapshot()
	// Sync DB query count from repo if repo is primary source
	if queries > snap.DatabaseQueries {
		snap.DatabaseQueries = queries
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (h *Handler) ResetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.tracker.Reset()
	h.repo.ResetMetrics()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"reset_ok"}`))
}

func (h *Handler) InvalidateCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /cache/invalidate/user/:id
	path := strings.TrimPrefix(r.URL.Path, "/cache/invalidate/user/")
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	if err := h.svc.InvalidateUser(r.Context(), id); err != nil {
		http.Error(w, fmt.Sprintf("failed to invalidate cache: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"invalidated","user_id":%d}`, id)))
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /users/:id
	path := strings.TrimPrefix(r.URL.Path, "/users/")
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	// Determine mode: query param > header > defaultMode
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = r.Header.Get("X-Mode")
	}
	if mode == "" {
		mode = h.defaultMode
	}

	var user *database.User
	var fetchErr error

	switch mode {
	case "singleflight":
		user, fetchErr = h.svc.GetUserWithSingleflight(r.Context(), id)
	case "redis-lock":
		user, fetchErr = h.svc.GetUserWithRedisLock(r.Context(), id)
	default: // "baseline"
		user, fetchErr = h.svc.GetUserWithoutSingleflight(r.Context(), id)
	}

	if fetchErr != nil {
		http.Error(w, fmt.Sprintf("error: %v", fetchErr), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Mode-Used", mode)
	_ = json.NewEncoder(w).Encode(user)
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Cache Stampede Demo Dashboard</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 30px; background: #0f172a; color: #f8fafc; }
        h1 { color: #38bdf8; font-size: 24px; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-top: 20px; }
        .card { background: #1e293b; padding: 20px; border-radius: 8px; border: 1px solid #334155; }
        .label { font-size: 13px; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.05em; }
        .value { font-size: 32px; font-weight: bold; margin-top: 8px; color: #f1f5f9; }
        .highlight { color: #38bdf8; }
        .warn { color: #f43f5e; }
        .success { color: #10b981; }
        .actions { margin-top: 24px; }
        button { background: #3b82f6; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: 500; }
        button:hover { background: #2563eb; }
    </style>
</head>
<body>
    <h1>⚡ Cache Stampede vs Singleflight Live Metrics</h1>
    <div class="grid">
        <div class="card"><div class="label">Total Requests</div><div id="total_requests" class="value">0</div></div>
        <div class="card"><div class="label">Cache Hits</div><div id="cache_hits" class="value success">0</div></div>
        <div class="card"><div class="label">Cache Misses</div><div id="cache_misses" class="value warn">0</div></div>
        <div class="card"><div class="label">Database Queries</div><div id="database_queries" class="value warn">0</div></div>
        <div class="card"><div class="label">Max DB Concurrency</div><div id="max_db_concurrency" class="value">0</div></div>
        <div class="card"><div class="label">Singleflight Calls</div><div id="singleflight_calls" class="value highlight">0</div></div>
        <div class="card"><div class="label">Singleflight Shared</div><div id="singleflight_shared" class="value success">0</div></div>
        <div class="card"><div class="label">Errors</div><div id="errors" class="value">0</div></div>
    </div>
    <div class="actions">
        <button onclick="resetStats()">Reset Metrics</button>
    </div>
    <script>
        async function fetchStats() {
            try {
                const res = await fetch('/stats');
                const data = await res.json();
                for (const [k, v] of Object.entries(data)) {
                    const el = document.getElementById(k);
                    if (el) el.innerText = Number(v).toLocaleString();
                }
            } catch (e) {}
        }
        async function resetStats() {
            await fetch('/stats/reset', { method: 'POST' });
            fetchStats();
        }
        setInterval(fetchStats, 500);
        fetchStats();
    </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}
