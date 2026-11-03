package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestPasswordQueriesEndToEnd(t *testing.T) {
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

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		FirstName: "Петр",
		LastName:  "Петров",
		Email:     "petr@example.com",
		Role:      "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const firstHash = "$argon2id$v=19$m=65536,t=1,p=1$hash1"
	const secondHash = "$argon2id$v=19$m=65536,t=1,p=1$hash2"

	updated, err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           user.ID,
		PasswordHash: pgtype.Text{String: firstHash, Valid: true},
	})
	if err != nil {
		t.Fatalf("update password: %v", err)
	}
	if updated.PasswordHash.String != firstHash {
		t.Errorf("expected stored hash %q, got %q", firstHash, updated.PasswordHash.String)
	}

	for _, h := range []string{secondHash} {
		if _, err := q.InsertPasswordHistory(ctx, db.InsertPasswordHistoryParams{
			UserID:       user.ID,
			PasswordHash: h,
		}); err != nil {
			t.Fatalf("insert history: %v", err)
		}
	}

	hashes, err := q.ListRecentPasswordHashes(ctx, db.ListRecentPasswordHashesParams{
		UserID: user.ID,
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hashes) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(hashes))
	}

	found := map[string]bool{}
	for _, h := range hashes {
		found[h] = true
	}
	if !found[firstHash] || !found[secondHash] {
		t.Errorf("history missing hashes: got %v", hashes)
	}

	// The stored user hash remains the one set via UpdateUserPassword; history
	// entries do not alter the users record.
	noPass, err := q.GetUserByEmail(ctx, "petr@example.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !noPass.PasswordHash.Valid || noPass.PasswordHash.String != firstHash {
		t.Errorf("expected stored password hash %q, got %v", firstHash, noPass.PasswordHash)
	}

	if err := MigrateDown(ctx, dsn, ""); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}
