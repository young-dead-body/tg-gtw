package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestPasswordChangedAt(t *testing.T) {
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

	created, err := q.CreateUser(ctx, db.CreateUserParams{
		FirstName:    "Кирилл",
		LastName:     "Кириллов",
		Email:        "kirill@example.com",
		Role:         "user",
		PasswordHash: pgtype.Text{String: "hash-1", Valid: true},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if created.PasswordChangedAt.Valid {
		t.Error("expected password_changed_at to be NULL after create via raw query")
	}

	if err := q.TouchPasswordChangedAt(ctx, created.ID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	touched, err := q.GetUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !touched.PasswordChangedAt.Valid {
		t.Error("expected password_changed_at to be set after touch")
	}

	// UpdateUserPassword resets the change timestamp.
	time.Sleep(10 * time.Millisecond)
	if _, err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           created.ID,
		PasswordHash: pgtype.Text{String: "hash-2", Valid: true},
	}); err != nil {
		t.Fatalf("update password: %v", err)
	}
	rotated, err := q.GetUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !rotated.PasswordChangedAt.Valid || !rotated.PasswordChangedAt.Time.After(touched.PasswordChangedAt.Time) {
		t.Errorf("expected password_changed_at to move forward: %v -> %v",
			touched.PasswordChangedAt.Time, rotated.PasswordChangedAt.Time)
	}
}
