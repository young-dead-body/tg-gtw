package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// TestArchiveQueries verifies the atomic move of old audit_log rows into the
// audit_log_archive table (T16.3).
func TestArchiveQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := testDSN(t)
	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Logf("clean down: %v", err)
	}
	if err := Migrate(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := db.New(pool)

	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_log (action, created_at) VALUES ('login.success', now() - interval '120 days')`); err != nil {
		t.Fatalf("insert old log: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_log (action, created_at) VALUES ('logout', now() - interval '1 day')`); err != nil {
		t.Fatalf("insert fresh log: %v", err)
	}

	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-90 * 24 * time.Hour), Valid: true}

	count, err := q.CountOldAuditLogs(ctx, cutoff)
	if err != nil {
		t.Fatalf("count old: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 old log, got %d", count)
	}

	if err := q.ArchiveOldAuditLogs(ctx, cutoff); err != nil {
		t.Fatalf("archive: %v", err)
	}

	var archived int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log_archive`).Scan(&archived); err != nil {
		t.Fatalf("count archive: %v", err)
	}
	if archived != 1 {
		t.Errorf("expected 1 archived row, got %d", archived)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("expected 1 fresh log to remain, got %d", remaining)
	}

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}
