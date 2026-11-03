// Package ratelimit implements rate limiting and brute-force lockout on top
// of a shared counter store (Redis in production, memory in tests).
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Store is a counter key-value store with TTL.
type Store interface {
	// Incr increments the counter for key, sets ttl when the key is new and
	// returns the new value.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// Get returns the current value of key (0 when absent).
	Get(ctx context.Context, key string) (int64, error)
	// Reset removes the key.
	Reset(ctx context.Context, key string) error
}

// Limiter allows a fixed number of requests per window for a resource id.
type Limiter struct {
	store  Store
	limit  int64
	window time.Duration
	prefix string
}

// NewLimiter builds a limiter: at most limit hits per window per id.
func NewLimiter(store Store, prefix string, limit int64, window time.Duration) *Limiter {
	return &Limiter{store: store, prefix: prefix, limit: limit, window: window}
}

// Allow records a hit and reports whether the caller stays within the limit.
func (l *Limiter) Allow(ctx context.Context, id string) (bool, error) {
	n, err := l.store.Incr(ctx, l.prefix+id, l.window)
	if err != nil {
		return false, err
	}
	return n <= l.limit, nil
}

// Lockout blocks an id after a number of consecutive failures for the window.
type Lockout struct {
	store     Store
	threshold int64
	window    time.Duration
}

// NewLockout builds a lockout: blocked after threshold failures in window.
func NewLockout(store Store, threshold int64, window time.Duration) *Lockout {
	return &Lockout{store: store, threshold: threshold, window: window}
}

// Blocked reports whether the id is currently locked out.
func (l *Lockout) Blocked(ctx context.Context, id string) (bool, error) {
	n, err := l.store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	return n >= l.threshold, nil
}

// RecordFailure counts a failed attempt.
func (l *Lockout) RecordFailure(ctx context.Context, id string) error {
	_, err := l.store.Incr(ctx, id, l.window)
	return err
}

// Reset clears failures for the id (called on a successful login).
func (l *Lockout) Reset(ctx context.Context, id string) error {
	return l.store.Reset(ctx, id)
}

// MemoryStore is an in-memory Store for tests and single-process use.
type MemoryStore struct {
	mu     sync.Mutex
	counts map[string]countEntry
	now    func() time.Time
}

type countEntry struct {
	value int64
	until time.Time
}

// NewMemoryStore builds an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{counts: map[string]countEntry{}, now: time.Now}
}

func (m *MemoryStore) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	entry, ok := m.counts[key]
	if !ok || now.After(entry.until) {
		entry = countEntry{}
	}
	entry.value++
	entry.until = now.Add(ttl)
	m.counts[key] = entry
	return entry.value, nil
}

func (m *MemoryStore) Get(ctx context.Context, key string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.counts[key]
	if !ok || m.now().After(entry.until) {
		return 0, nil
	}
	return entry.value, nil
}

func (m *MemoryStore) Reset(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.counts, key)
	return nil
}

var _ Store = (*MemoryStore)(nil)
