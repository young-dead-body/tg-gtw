package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestAdminUserQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := testDSN(t)

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Logf("clean down before test: %v", err)
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
		FirstName:    "Ирина",
		LastName:     "Сидорова",
		Email:        "irina@example.com",
		Role:         "user",
		PasswordHash: pgtype.Text{String: "hash-placeholder", Valid: true},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Update the profile: rename, promote, expire.
	expiresAt := pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true}
	updated, err := q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		ID:           created.ID,
		FirstName:    "Ирина",
		LastName:     "Сидорова",
		MiddleName:   pgtype.Text{String: "Сергеевна", Valid: true},
		Telegram:     pgtype.Text{String: "@irina", Valid: true},
		Department:   pgtype.Text{String: "Бухгалтерия", Valid: true},
		Position:     pgtype.Text{String: "Главбух", Valid: true},
		Role:         "admin",
		IsActive:     true,
		BlockedAt:    pgtype.Timestamptz{},
		ExpiresAt:    expiresAt,
		PasswordHash: created.PasswordHash,
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.Role != "admin" {
		t.Errorf("expected role admin, got %q", updated.Role)
	}
	if !updated.MiddleName.Valid || updated.MiddleName.String != "Сергеевна" {
		t.Errorf("expected updated middle name, got %v", updated.MiddleName)
	}
	if !updated.ExpiresAt.Valid || updated.ExpiresAt.Time.Sub(expiresAt.Time).Abs() > time.Millisecond {
		t.Errorf("expected expiry preserved, got %v want %v", updated.ExpiresAt, expiresAt)
	}

	// Blocking sets the flag and timestamp.
	blocked, err := q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		ID:           created.ID,
		FirstName:    updated.FirstName,
		LastName:     updated.LastName,
		MiddleName:   updated.MiddleName,
		Telegram:     updated.Telegram,
		Department:   updated.Department,
		Position:     updated.Position,
		Role:         updated.Role,
		IsActive:     false,
		BlockedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		ExpiresAt:    expiresAt,
		PasswordHash: updated.PasswordHash,
	})
	if err != nil {
		t.Fatalf("block user: %v", err)
	}
	if blocked.IsActive {
		t.Error("expected user to be inactive after blocking")
	}
	if !blocked.BlockedAt.Valid {
		t.Error("expected blocked_at to be set")
	}

	// List returns the user.
	users, err := q.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	found := false
	for _, u := range users {
		if u.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("expected user in ListUsers")
	}

	// Delete removes the row.
	deleted, err := q.DeleteUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if deleted.ID != created.ID {
		t.Errorf("deleted wrong user: %v != %v", deleted.ID, created.ID)
	}
	if _, err := q.GetUserByID(ctx, created.ID); err == nil {
		t.Error("expected GetUserByID to fail after delete")
	}
}
