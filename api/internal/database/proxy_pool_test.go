package database

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func TestProxyPoolQueries(t *testing.T) {
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

	// ListActiveProxies returns nothing initially.
	proxies, err := q.ListActiveProxies(ctx)
	if err != nil {
		t.Fatalf("list active proxies: %v", err)
	}
	if len(proxies) != 0 {
		t.Fatalf("expected empty pool, got %d", len(proxies))
	}

	// Insert two proxies (active and inactive) via InsertProxyPool.
	active, err := q.InsertProxyPool(ctx, db.InsertProxyPoolParams{
		Url:      "http://telegram-web-1:8080",
		Status:   "active",
		Priority: 1,
	})
	if err != nil {
		t.Fatalf("insert active proxy: %v", err)
	}
	_, err = q.InsertProxyPool(ctx, db.InsertProxyPoolParams{
		Url:      "http://telegram-web-2:8080",
		Status:   "inactive",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("insert inactive proxy: %v", err)
	}

	proxies, err = q.ListActiveProxies(ctx)
	if err != nil {
		t.Fatalf("list active proxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("expected 1 active proxy, got %d", len(proxies))
	}
	if proxies[0].ID != active.ID {
		t.Fatalf("unexpected active proxy id %v", proxies[0].ID)
	}

	// UpdateProxyHealth demotes the active proxy and refreshes last_checked_at.
	if err := q.UpdateProxyHealth(ctx, db.UpdateProxyHealthParams{
		ID:     active.ID,
		Status: "inactive",
	}); err != nil {
		t.Fatalf("update proxy health: %v", err)
	}
	proxies, err = q.ListActiveProxies(ctx)
	if err != nil {
		t.Fatalf("list active proxies: %v", err)
	}
	if len(proxies) != 0 {
		t.Fatalf("expected empty pool after demotion, got %d", len(proxies))
	}
}

func TestConsentLinking(t *testing.T) {
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

	ip := netip.MustParseAddr("198.51.100.7")
	ua := "Mozilla/5.0 (compatible; gateway-test)"

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		FirstName: "Анна",
		LastName:  "Антонова",
		Email:     "anna@example.com",
		Role:      "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// A user who accepted no consent has no consent.
	has, err := q.HasUserConsent(ctx, user.ID)
	if err != nil {
		t.Fatalf("has consent: %v", err)
	}
	if has {
		t.Fatal("expected no consent before accepting")
	}

	// Anonymous consent is linked only by the unguessable receipt ID.
	anonConsent, err := q.InsertConsent(ctx, db.InsertConsentParams{
		DocumentVersion: "1.0", Ip: &ip, UserAgent: pgtype.Text{String: ua, Valid: true},
	})
	if err != nil {
		t.Fatalf("insert consent: %v", err)
	}

	// Another user on a different device has no consent.
	other, err := q.CreateUser(ctx, db.CreateUserParams{
		FirstName: "Борис",
		LastName:  "Борисов",
		Email:     "boris@example.com",
		Role:      "user",
	})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}

	// Linking attaches the anonymous consent to the user that just logged in.
	if err := q.LinkConsentToUser(ctx, db.LinkConsentToUserParams{
		UserID: user.ID, ConsentID: anonConsent.ID,
	}); err != nil {
		t.Fatalf("link consent: %v", err)
	}

	has, err = q.HasUserConsent(ctx, user.ID)
	if err != nil {
		t.Fatalf("has consent after link: %v", err)
	}
	if !has {
		t.Fatal("expected consent after linking")
	}

	has, err = q.HasUserConsent(ctx, other.ID)
	if err != nil {
		t.Fatalf("other has consent: %v", err)
	}
	if has {
		t.Fatal("other user must not have consent")
	}
}
