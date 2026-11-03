package cron

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type fakeArchiveStore struct {
	count    int64
	countErr error
	archived bool
	execErr  error
	cutoff   pgtype.Timestamptz
}

func (f *fakeArchiveStore) CountOldAuditLogs(_ context.Context, c pgtype.Timestamptz) (int64, error) {
	f.cutoff = c
	return f.count, f.countErr
}

func (f *fakeArchiveStore) ArchiveOldAuditLogs(_ context.Context, c pgtype.Timestamptz) error {
	f.archived = true
	f.cutoff = c
	return f.execErr
}

func TestLogArchiverSkipsWhenEmpty(t *testing.T) {
	store := &fakeArchiveStore{}
	a := &LogArchiver{Queries: store}
	n, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 archived, got %d", n)
	}
	if store.archived {
		t.Error("archive query must not run when nothing to archive")
	}
}

func TestLogArchiverMovesOldRows(t *testing.T) {
	now := time.Now()
	store := &fakeArchiveStore{count: 4}
	a := &LogArchiver{Queries: store, Age: 90 * 24 * time.Hour, Now: func() time.Time { return now }}
	n, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 archived, got %d", n)
	}
	if !store.archived {
		t.Error("expected archive query to run")
	}
	want := now.Add(-90 * 24 * time.Hour)
	if !store.cutoff.Time.Equal(want) {
		t.Errorf("expected cutoff %v, got %v", want, store.cutoff.Time)
	}
}

func TestLogArchiverPropagatesCountError(t *testing.T) {
	store := &fakeArchiveStore{countErr: errBoom{}}
	a := &LogArchiver{Queries: store}
	_, err := a.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from count, got nil")
	}
}

func TestLogArchiverPropagatesArchiveError(t *testing.T) {
	store := &fakeArchiveStore{count: 2, execErr: errBoom{}}
	a := &LogArchiver{Queries: store}
	_, err := a.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from archive, got nil")
	}
}
