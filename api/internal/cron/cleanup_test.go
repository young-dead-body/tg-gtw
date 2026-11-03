package cron

import (
	"context"
	"testing"
	"time"
)

type fakeCleanupStore struct {
	expired  int64
	requests int64
	delErr   error
	gotSecs  float64
}

func (f *fakeCleanupStore) DeleteExpiredSessions(context.Context) (int64, error) {
	return f.expired, f.delErr
}

func (f *fakeCleanupStore) DeleteOldRegistrationRequests(_ context.Context, secs float64) (int64, error) {
	f.gotSecs = secs
	return f.requests, f.delErr
}

func TestCleanupRemovesExpiredAndStale(t *testing.T) {
	store := &fakeCleanupStore{expired: 3, requests: 5}
	c := &Cleanup{Queries: store}
	total, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if total != 8 {
		t.Errorf("expected 8 removed, got %d", total)
	}
	// Default request age is 30 days.
	if want := float64(30 * 24 * 3600); store.gotSecs != want {
		t.Errorf("expected request age %v sec, got %v", want, store.gotSecs)
	}
}

func TestCleanupCustomAge(t *testing.T) {
	store := &fakeCleanupStore{expired: 1, requests: 2}
	c := &Cleanup{Queries: store, RequestAge: 7 * 24 * time.Hour}
	total, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if total != 3 {
		t.Errorf("expected 3 deletions, got %d", total)
	}
	if want := (7 * 24 * time.Hour).Seconds(); store.gotSecs != want {
		t.Errorf("expected age %v sec, got %v", want, store.gotSecs)
	}
}

func TestCleanupPropagatesError(t *testing.T) {
	store := &fakeCleanupStore{delErr: errBoom{}}
	c := &Cleanup{Queries: store}
	_, err := c.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
