package database

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestConsentQueries(t *testing.T) {
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

	ip := netip.MustParseAddr("192.0.2.10")
	consent, err := q.InsertConsent(ctx, db.InsertConsentParams{
		DocumentVersion: "1.0",
		Ip:              &ip,
		UserAgent:       pgtype.Text{String: "Mozilla/5.0", Valid: true},
	})
	if err != nil {
		t.Fatalf("insert consent: %v", err)
	}
	if consent.DocumentVersion != "1.0" {
		t.Errorf("expected version 1.0, got %q", consent.DocumentVersion)
	}
	if consent.Ip.String() != "192.0.2.10" {
		t.Errorf("expected ip 192.0.2.10, got %q", consent.Ip.String())
	}
	if !consent.UserAgent.Valid || consent.UserAgent.String != "Mozilla/5.0" {
		t.Errorf("expected user agent, got %v", consent.UserAgent)
	}
	if consent.AcceptedAt.Time.IsZero() {
		t.Error("expected accepted_at to be set")
	}

	// Consent can be tied to a user.
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		FirstName: "Олег",
		LastName:  "Олегов",
		Email:     "oleg@example.com",
		Role:      "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	withUser, err := q.InsertConsent(ctx, db.InsertConsentParams{
		UserID:          user.ID,
		DocumentVersion: "1.0",
		Ip:              &ip,
		UserAgent:       pgtype.Text{String: "curl", Valid: true},
	})
	if err != nil {
		t.Fatalf("insert consent with user: %v", err)
	}
	if withUser.UserID != user.ID {
		t.Errorf("expected user_id to be recorded, got %v", withUser.UserID)
	}
}
