package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cache-stampede-demo/internal/database"
	"github.com/redis/go-redis/v9"
)

var ErrCacheMiss = errors.New("cache miss")

type CacheClient interface {
	GetUser(ctx context.Context, id int64) (*database.User, error)
	SetUser(ctx context.Context, user *database.User, ttl time.Duration) error
	InvalidateUser(ctx context.Context, id int64) error
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	ReleaseLock(ctx context.Context, key string) error
	Close() error
}

type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(addr string, password string, db int) (*RedisClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
		PoolSize: 200,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisClient{client: rdb}, nil
}

func (r *RedisClient) userKey(id int64) string {
	return fmt.Sprintf("user:%d", id)
}

func (r *RedisClient) GetUser(ctx context.Context, id int64) (*database.User, error) {
	val, err := r.client.Get(ctx, r.userKey(id)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrCacheMiss
		}
		return nil, err
	}

	var u database.User
	if err := json.Unmarshal([]byte(val), &u); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached user: %w", err)
	}

	return &u, nil
}

func (r *RedisClient) SetUser(ctx context.Context, user *database.User, ttl time.Duration) error {
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}
	return r.client.Set(ctx, r.userKey(user.ID), data, ttl).Err()
}

func (r *RedisClient) InvalidateUser(ctx context.Context, id int64) error {
	return r.client.Del(ctx, r.userKey(id)).Err()
}

func (r *RedisClient) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	lockKey := fmt.Sprintf("lock:%s", key)
	return r.client.SetNX(ctx, lockKey, "1", ttl).Result()
}

func (r *RedisClient) ReleaseLock(ctx context.Context, key string) error {
	lockKey := fmt.Sprintf("lock:%s", key)
	return r.client.Del(ctx, lockKey).Err()
}

func (r *RedisClient) Close() error {
	return r.client.Close()
}
