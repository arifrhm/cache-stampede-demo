package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"cache-stampede-demo/internal/cache"
	"cache-stampede-demo/internal/database"
	"cache-stampede-demo/internal/handler"
	"cache-stampede-demo/internal/metrics"
	"cache-stampede-demo/internal/service"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func main() {
	port := getEnv("PORT", "8880")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnvInt("DB_PORT", 5433)
	dbUser := getEnv("DB_USER", "postgres")
	dbPass := getEnv("DB_PASSWORD", "postgres")
	dbName := getEnv("DB_NAME", "stampededb")
	dbMaxConns := getEnvInt("DB_MAX_CONNS", 20)
	dbLatencyMs := getEnvInt("DB_LATENCY_MS", 100)
	redisAddr := getEnv("REDIS_ADDR", "localhost:6380")
	redisPass := getEnv("REDIS_PASSWORD", "")
	redisDB := getEnvInt("REDIS_DB", 0)
	cacheTTLSeconds := getEnvInt("CACHE_TTL_SECONDS", 2)
	jitterMs := getEnvInt("CACHE_TTL_JITTER_MS", 0)
	defaultMode := getEnv("DEFAULT_MODE", "baseline")

	log.Printf("[SERVER] Starting on port %s", port)
	log.Printf("[CONFIG] DB=%s:%d (max_conns=%d, latency=%dms) Redis=%s (TTL=%ds)",
		dbHost, dbPort, dbMaxConns, dbLatencyMs, redisAddr, cacheTTLSeconds)

	// Initialize Database Repository
	repo, err := database.NewPostgresRepository(database.DBConfig{
		Host:         dbHost,
		Port:         dbPort,
		User:         dbUser,
		Password:     dbPass,
		DBName:       dbName,
		SSLMode:      "disable",
		MaxConns:     dbMaxConns,
		MaxIdleConns: dbMaxConns,
		LatencyMs:    dbLatencyMs,
	})
	if err != nil {
		log.Fatalf("[FATAL] Database connection failed: %v", err)
	}
	defer repo.Close()

	// Initialize Redis Client
	redisClient, err := cache.NewRedisClient(redisAddr, redisPass, redisDB)
	if err != nil {
		log.Fatalf("[FATAL] Redis connection failed: %v", err)
	}
	defer redisClient.Close()

	tracker := metrics.NewTracker()
	ttl := time.Duration(cacheTTLSeconds) * time.Second
	jitter := time.Duration(jitterMs) * time.Millisecond
	userService := service.NewUserService(repo, redisClient, tracker, ttl, jitter)

	h := handler.NewHandler(userService, repo, tracker, defaultMode)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[FATAL] Server error: %v", err)
		}
	}()

	log.Printf("[SERVER] HTTP server ready at http://localhost:%s", port)
	<-stop

	log.Println("[SERVER] Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("[ERROR] Server shutdown error: %v", err)
	}
	log.Println("[SERVER] Stopped.")
}
