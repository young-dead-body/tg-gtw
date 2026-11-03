package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// TestCleanupQueries verifies expiring-session and stale-request deletion.
func TestCleanupQueries(t *testing.T) {
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

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		Email:        "clean@example.com",
		FirstName:    "Clean",
		LastName:     "User",
		Role:         "user",
		PasswordHash: pgtype.Text{String: "x", Valid: true},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// A stale request and a fresh pending request.
	if _, err := pool.Exec(ctx,
		`INSERT INTO registration_requests (first_name,last_name,email,status,created_at)
		 VALUES ('Old','Req','old@example.com','pending', now() - interval '40 days')`); err != nil {
		t.Fatalf("insert stale request: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO registration_requests (first_name,last_name,email,status,created_at)
		 VALUES ('New','Req','new@example.com','pending', now())`); err != nil {
		t.Fatalf("insert fresh request: %v", err)
	}

	// An expired and a live session.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, expires_at) VALUES ($1, now() - interval '1 hour')`, user.ID); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, expires_at) VALUES ($1, now() + interval '1 hour')`, user.ID); err != nil {
		t.Fatalf("insert live session: %v", err)
	}

	expired, err := q.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("delete expired sessions: %v", err)
	}
	if expired != 1 {
		t.Errorf("expected 1 expired session, got %d", expired)
	}

	removed, err := q.DeleteOldRegistrationRequests(ctx, 30*24*3600.0)
	if err != nil {
		t.Fatalf("delete old requests: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 stale request removed, got %d", removed)
	}

	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM registration_requests WHERE email='new@example.com'`).Scan(&remaining); err != nil {
		t.Fatalf("count fresh request: %v", err)
	}
	if remaining != 1 {
		t.Errorf("fresh request must remain, got %d", remaining)
	}

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}
