package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
)

// bootstrapAdmin creates the first administrator exactly once. Credentials are
// printed only after all database changes have committed successfully.
func bootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, hasher password.Hasher, output io.Writer) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin admin bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize startup across API replicas, so two empty-database starts do not
	// create two administrators with different credentials.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('telegram-gateway-bootstrap-admin'))"); err != nil {
		return fmt.Errorf("lock admin bootstrap: %w", err)
	}
	queries := db.New(tx)
	users, err := queries.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	for _, user := range users {
		if user.Role == "admin" {
			return nil
		}
	}

	loginBytes := make([]byte, 8)
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(loginBytes); err != nil {
		return fmt.Errorf("generate admin login: %w", err)
	}
	if _, err := rand.Read(passwordBytes); err != nil {
		return fmt.Errorf("generate admin password: %w", err)
	}
	email := "admin-" + hex.EncodeToString(loginBytes) + "@example.com"
	plain := hex.EncodeToString(passwordBytes)
	hash, err := hasher.Hash(plain)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		FirstName:    "System",
		LastName:     "Administrator",
		Email:        email,
		Role:         "admin",
		PasswordHash: pgtype.Text{String: hash, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	if _, err := queries.InsertPasswordHistory(ctx, db.InsertPasswordHistoryParams{
		UserID:       user.ID,
		PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("insert password history: %w", err)
	}
	if err := queries.TouchPasswordChangedAt(ctx, user.ID); err != nil {
		return fmt.Errorf("record password change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit admin bootstrap: %w", err)
	}

	_, err = fmt.Fprintf(output, "BOOTSTRAP_ADMIN_EMAIL=%s\nBOOTSTRAP_ADMIN_PASSWORD=%s\n", email, plain)
	if err != nil {
		return fmt.Errorf("print admin credentials: %w", err)
	}
	return nil
}
