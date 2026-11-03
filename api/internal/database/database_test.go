package database

import (
	"context"
	"os"
	"testing"
	"time"
)

// These are integration tests that require a real PostgreSQL instance.
// They are skipped unless TEST_DATABASE_URL is set, e.g.:
//
//	TEST_DATABASE_URL=postgres://tg@localhost:5432/telegram_gateway_test go test ./internal/database
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	return dsn
}

func TestMigrationsUpAndDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := testDSN(t)

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Logf("clean down before test: %v", err)
	}

	if err := Migrate(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	// Tables should now exist.
	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{
		"users", "sessions", "registration_requests", "audit_log",
		"statistics", "consents", "password_history", "settings", "proxy_pool",
	}
	for _, tbl := range tables {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, tbl).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("expected table %q to exist", tbl)
		}
	}

	// Clean up so the test is idempotent.
	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}

func TestConnectFailsOnBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Connect(ctx, "postgres://tg@localhost:1/nonexistent"); err == nil {
		t.Fatal("expected error for unreachable database")
	}
}
