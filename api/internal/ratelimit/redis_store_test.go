package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedisStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisStore(client), server
}

func TestRedisStoreIncrAndTTL(t *testing.T) {
	store, server := newRedisStore(t)

	if _, err := store.Incr(context.Background(), "k", time.Minute); err != nil {
		t.Fatalf("incr: %v", err)
	}
	if _, err := store.Incr(context.Background(), "k", time.Minute); err != nil {
		t.Fatalf("incr: %v", err)
	}
	n, err := store.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2, got %d", n)
	}

	ttl := server.TTL("k")
	if ttl <= 0 || ttl > time.Minute {
		t.Errorf("expected ttl ~1m, got %v", ttl)
	}
}

func TestRedisStoreGetMissing(t *testing.T) {
	store, _ := newRedisStore(t)
	n, err := store.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestRedisStoreReset(t *testing.T) {
	store, _ := newRedisStore(t)
	ctx := context.Background()
	if _, err := store.Incr(ctx, "k", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(ctx, "k"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	n, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 after reset, got %d", n)
	}
}
