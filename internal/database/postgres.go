package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
)

type UserRepository interface {
	GetUser(ctx context.Context, id int64) (*User, error)
	GetMetrics() (queries int64, maxConcurrency int64, currentConcurrency int64)
	ResetMetrics()
	Close() error
}

type PostgresRepository struct {
	db                 *sql.DB
	latency            time.Duration
	queryCount         atomic.Int64
	currentConcurrency atomic.Int64
	maxConcurrency     atomic.Int64
}

type DBConfig struct {
	Host         string
	Port         int
	User         string
	Password     string
	DBName       string
	SSLMode      string
	MaxConns     int
	MaxIdleConns int
	LatencyMs    int
}

func NewPostgresRepository(cfg DBConfig) (*PostgresRepository, error) {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, sslMode)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	maxConns := cfg.MaxConns
	if maxConns <= 0 {
		maxConns = 20
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxConns
	}

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &PostgresRepository{
		db:      db,
		latency: time.Duration(cfg.LatencyMs) * time.Millisecond,
	}, nil
}

func (r *PostgresRepository) GetUser(ctx context.Context, id int64) (*User, error) {
	// Track DB query count
	r.queryCount.Add(1)

	// Track concurrency
	cur := r.currentConcurrency.Add(1)
	defer r.currentConcurrency.Add(-1)

	// Update max concurrency observed
	for {
		max := r.maxConcurrency.Load()
		if cur <= max {
			break
		}
		if r.maxConcurrency.CompareAndSwap(max, cur) {
			break
		}
	}

	// Actual PostgreSQL query:
	// SELECT id, name, email FROM users WHERE id = $1;
	query := "SELECT id, name, email FROM users WHERE id = $1;"
	row := r.db.QueryRowContext(ctx, query, id)

	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.Email); err != nil {
		return nil, err
	}

	// Artificial latency to simulate realistic or heavy database load
	if r.latency > 0 {
		select {
		case <-time.After(r.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &u, nil
}

func (r *PostgresRepository) GetMetrics() (queries int64, maxConcurrency int64, currentConcurrency int64) {
	return r.queryCount.Load(), r.maxConcurrency.Load(), r.currentConcurrency.Load()
}

func (r *PostgresRepository) ResetMetrics() {
	r.queryCount.Store(0)
	r.maxConcurrency.Store(0)
	// Don't reset currentConcurrency if requests are active, but for clean state it drains
}

func (r *PostgresRepository) Close() error {
	return r.db.Close()
}
