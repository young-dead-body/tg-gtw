package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestLimiterAllowsUpToLimit(t *testing.T) {
	store := NewMemoryStore()
	l := NewLimiter(store, "rl:", 3, time.Minute)

	ok := make([]bool, 4)
	for i := range ok {
		allowed, err := l.Allow(context.Background(), "user-1")
		if err != nil {
			t.Fatalf("allow: %v", err)
		}
		ok[i] = allowed
	}

	if !ok[0] || !ok[1] || !ok[2] {
		t.Errorf("expected first 3 hits allowed, got %v", ok)
	}
	if ok[3] {
		t.Errorf("expected 4th hit blocked, got %v", ok)
	}
}

func TestLimiterSeparateIds(t *testing.T) {
	store := NewMemoryStore()
	l := NewLimiter(store, "rl:", 2, time.Minute)

	for i := 0; i < 2; i++ {
		if _, err := l.Allow(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
	}
	allowed, err := l.Allow(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("expected separate counter for different id")
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	store := NewMemoryStore()
	store.now = func() time.Time { return time.Unix(1000, 0) }
	l := NewLimiter(store, "rl:", 1, time.Minute)

	if _, err := l.Allow(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}

	store.now = func() time.Time { return time.Unix(1000+61, 0) }
	allowed, err := l.Allow(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("expected window to reset after ttl")
	}
}

func TestLockoutBlocksAfterThreshold(t *testing.T) {
	store := NewMemoryStore()
	l := NewLockout(store, 3, 15*time.Minute)

	for i := 0; i < 2; i++ {
		if err := l.RecordFailure(context.Background(), "ip:1.2.3.4"); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := l.Blocked(context.Background(), "ip:1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected not blocked before threshold")
	}

	if err := l.RecordFailure(context.Background(), "ip:1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	blocked, err = l.Blocked(context.Background(), "ip:1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked at threshold")
	}
}

func TestLockoutReset(t *testing.T) {
	store := NewMemoryStore()
	l := NewLockout(store, 3, 15*time.Minute)
	for i := 0; i < 3; i++ {
		if err := l.RecordFailure(context.Background(), "ip:1.2.3.4"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Reset(context.Background(), "ip:1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	blocked, err := l.Blocked(context.Background(), "ip:1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("expected reset to unblock")
	}
}
