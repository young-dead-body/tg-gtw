package ratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store with Redis counters. TTLs are only set when the
// key is created (INCR + EXPIRE NX), so every hit extends the window start.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore builds a store on the given Redis client.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func (s *RedisStore) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) (int64, error) {
	n, err := s.client.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *RedisStore) Reset(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

var _ Store = (*RedisStore)(nil)
