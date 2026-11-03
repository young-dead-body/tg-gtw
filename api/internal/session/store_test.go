package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (Store, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewRedisStore(client)

	cleanup := func() {
		_ = client.Close()
	}
	return store, cleanup
}

func TestCreateAndGet(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := store.Create(ctx, "user-1", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session id")
	}

	userID, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if userID != "user-1" {
		t.Errorf("expected user-1, got %q", userID)
	}
}

func TestGetUnknownSession(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := store.Create(ctx, "user-2", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	store := NewRedisStore(client)

	ctx := context.Background()
	id, err := store.Create(ctx, "user-3", time.Second)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	mr.FastForward(2 * time.Second)

	if _, err := store.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after expiry, got %v", err)
	}
}

func TestUniqueIDs(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	a, err := store.Create(ctx, "user-1", time.Hour)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := store.Create(ctx, "user-1", time.Hour)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a == b {
		t.Error("expected unique session ids")
	}
}

func TestDeleteUserRevokesAllSessions(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	for range 3 {
		if _, err := store.Create(ctx, "user-9", time.Hour); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	other, err := store.Create(ctx, "user-other", time.Hour)
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	if err := store.DeleteUser(ctx, "user-9"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// user-9 sessions are gone; the other user is untouched.
	if _, err := store.Get(ctx, "user-9"); err == nil {
		t.Fatal("expected a session to be revoked")
	}
	if uid, err := store.Get(ctx, other); err != nil || uid != "user-other" {
		t.Errorf("expected other session intact, got uid=%q err=%v", uid, err)
	}
}

func TestDeleteIndexesSession(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := store.Create(ctx, "user-5", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// After a single logout, DeleteUser must be a no-op without errors.
	if err := store.DeleteUser(ctx, "user-5"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
}
