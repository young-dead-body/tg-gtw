package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestHardeningQueries(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN(t)
	require.NoError(t, MigrateDown(ctx, dsn, ""))
	require.NoError(t, Migrate(ctx, dsn, ""))
	pool, err := Connect(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	q := db.New(pool)
	user, err := q.CreateUser(ctx, db.CreateUserParams{FirstName: "Test", LastName: "User", Email: "hardening@example.com", Role: "user", PasswordHash: pgtype.Text{String: "original", Valid: true}})
	require.NoError(t, err)
	consent, err := q.InsertConsent(ctx, db.InsertConsentParams{UserID: user.ID, DocumentVersion: "1.0"})
	require.NoError(t, err)
	ok, err := q.HasUserConsent(ctx, user.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, q.SetSetting(ctx, db.SetSettingParams{Key: "consent.document_version", Value: []byte(`"2.0"`)}))
	ok, err = q.HasUserConsent(ctx, user.ID)
	require.NoError(t, err)
	require.False(t, ok)
	// A stale receipt cannot be linked after the policy version changed.
	_, err = pool.Exec(ctx, "UPDATE consents SET user_id = NULL WHERE id = $1", consent.ID)
	require.NoError(t, err)
	require.NoError(t, q.LinkConsentToUser(ctx, db.LinkConsentToUserParams{UserID: user.ID, ConsentID: consent.ID}))
	var linked bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT user_id IS NOT NULL FROM consents WHERE id=$1", consent.ID).Scan(&linked))
	require.False(t, linked)
	// Database trigger makes password and history atomic even for direct SQL.
	_, err = q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: user.ID, PasswordHash: pgtype.Text{String: "changed", Valid: true}})
	require.NoError(t, err)
	hashes, err := q.ListRecentPasswordHashes(ctx, db.ListRecentPasswordHashesParams{UserID: user.ID, Limit: 5})
	require.NoError(t, err)
	require.Equal(t, []string{"changed"}, hashes)
	_, err = pool.Exec(ctx, "ALTER TABLE password_history ADD CONSTRAINT reject_test_hash CHECK (password_hash <> 'reject')")
	require.NoError(t, err)
	_, err = q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: user.ID, PasswordHash: pgtype.Text{String: "reject", Valid: true}})
	require.Error(t, err)
	current, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "changed", current.PasswordHash.String)
	_, err = pool.Exec(ctx, "ALTER TABLE password_history DROP CONSTRAINT reject_test_hash")
	require.NoError(t, err)
	// Token binding is based on immutable Telegram ID and is single-use.
	expiry := pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}
	require.NoError(t, q.IssueOneTimeToken(ctx, db.IssueOneTimeTokenParams{UserID: user.ID, Purpose: "telegram_link", TokenHash: "link-digest", ExpiresAt: expiry}))
	binding, err := q.BindTelegram(ctx, db.BindTelegramParams{TokenHash: "link-digest", TelegramUserID: 12345})
	require.NoError(t, err)
	require.Equal(t, user.ID, binding.UserID)
	_, err = q.BindTelegram(ctx, db.BindTelegramParams{TokenHash: "link-digest", TelegramUserID: 54321})
	require.Error(t, err)
	bound, err := q.GetUserByTelegramID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, user.ID, bound.ID)
	// Changing a password invalidates already issued reset links atomically.
	require.NoError(t, q.IssueOneTimeToken(ctx, db.IssueOneTimeTokenParams{UserID: user.ID, Purpose: "password_reset", TokenHash: "obsolete", ExpiresAt: expiry}))
	_, err = q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: user.ID, PasswordHash: pgtype.Text{String: "newer", Valid: true}})
	require.NoError(t, err)
	_, err = q.GetPasswordResetUser(ctx, "obsolete")
	require.Error(t, err)
	// A request authenticated by the former Telegram ID cannot mint a fresh
	// token after a concurrent rebind commits.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "UPDATE telegram_bindings SET telegram_user_id=54321,created_at=now() WHERE user_id=$1", user.ID)
	require.NoError(t, err)
	issued := make(chan error, 1)
	go func() {
		_, err := q.IssuePasswordResetToken(ctx, db.IssuePasswordResetTokenParams{TelegramUserID: 12345, TokenHash: "racing-old-id", ExpiresAt: expiry})
		issued <- err
	}()
	require.NoError(t, tx.Commit(ctx))
	select {
	case err := <-issued:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("issuance blocked after rebind")
	}
	_, err = q.GetPasswordResetUser(ctx, "racing-old-id")
	require.Error(t, err)

	// Concurrent redemption must change the password exactly once.
	require.NoError(t, q.IssueOneTimeToken(ctx, db.IssueOneTimeTokenParams{UserID: user.ID, Purpose: "password_reset", TokenHash: "reset-digest", ExpiresAt: expiry}))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := q.RedeemPasswordReset(ctx, db.RedeemPasswordResetParams{TokenHash: "reset-digest", PasswordHash: pgtype.Text{String: "reset", Valid: true}})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	hashes, err = q.ListRecentPasswordHashes(ctx, db.ListRecentPasswordHashesParams{UserID: user.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, hashes, 3)
}
