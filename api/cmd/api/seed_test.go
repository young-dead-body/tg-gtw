package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/company/telegram-gateway/api/internal/database"
	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
)

// Integration tests requiring a real PostgreSQL instance (see internal/database
// tests for the same convention):
//
//	TEST_DATABASE_URL=postgres://tg@localhost:5432/telegram_gateway_test go test ./cmd/api
func seedTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping seed integration test")
	}
	return dsn
}

func TestBootstrapAdmin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := seedTestDSN(t)
	if err := database.Migrate(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	q := db.New(pool)

	// Clean slate: no admins, no password history.
	if _, err := pool.Exec(ctx, `DELETE FROM password_history`); err != nil {
		t.Fatalf("clean password_history: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("clean users: %v", err)
	}

	// First run creates the admin.
	var output bytes.Buffer
	if err := bootstrapAdmin(ctx, pool, password.New(), &output); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !regexp.MustCompile(`^BOOTSTRAP_ADMIN_EMAIL=admin-[0-9a-f]{16}@example\.com$`).MatchString(lines[0]) || !regexp.MustCompile(`^BOOTSTRAP_ADMIN_PASSWORD=[0-9a-f]{48}$`).MatchString(lines[1]) {
		t.Fatalf("unexpected bootstrap credential format")
	}
	email := strings.TrimPrefix(lines[0], "BOOTSTRAP_ADMIN_EMAIL=")
	plain := strings.TrimPrefix(lines[1], "BOOTSTRAP_ADMIN_PASSWORD=")

	admins, err := q.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(admins) != 1 || admins[0].Role != "admin" || admins[0].Email != email {
		t.Fatalf("expected exactly one admin, got %+v", admins)
	}
	if !admins[0].PasswordHash.Valid {
		t.Fatal("expected admin password hash to be set")
	}
	if ok, _ := password.New().Verify(plain, admins[0].PasswordHash.String); !ok {
		t.Fatal("admin password does not verify")
	}

	// Idempotent: a second run must not create or override anything.
	output.Reset()
	if err := bootstrapAdmin(ctx, pool, password.New(), &output); err != nil {
		t.Fatalf("second bootstrap run: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("re-run printed credentials")
	}
	admins, err = q.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(admins) != 1 {
		t.Fatalf("expected one admin after re-run, got %d", len(admins))
	}
	if ok, _ := password.New().Verify(plain, admins[0].PasswordHash.String); !ok {
		t.Fatal("re-run overrode the existing admin password")
	}
}

func TestBootstrapAdminConcurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := seedTestDSN(t)
	if err := database.Migrate(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DELETE FROM password_history`); err != nil {
		t.Fatalf("clean password_history: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("clean users: %v", err)
	}

	var outputs [2]bytes.Buffer
	var results [2]error
	var wg sync.WaitGroup
	for i := range outputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = bootstrapAdmin(ctx, pool, password.New(), &outputs[i])
		}()
	}
	wg.Wait()
	for _, result := range results {
		if result != nil {
			t.Fatalf("concurrent bootstrap: %v", result)
		}
	}
	users, err := db.New(pool).ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 || users[0].Role != "admin" {
		t.Fatalf("expected one admin, got %d users", len(users))
	}
	printed := 0
	for _, output := range outputs {
		if output.Len() > 0 {
			printed++
		}
	}
	if printed != 1 {
		t.Fatalf("expected credentials printed once, got %d", printed)
	}
}
