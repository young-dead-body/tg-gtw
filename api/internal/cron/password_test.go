package cron

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/policy"
)

type fakeNotifier struct {
	events []string
}

func (f *fakeNotifier) Notify(_ context.Context, event string, _ string) error {
	f.events = append(f.events, event)
	return nil
}

type fakeQuerier struct {
	users []db.User
}

func (f *fakeQuerier) ListUsers(context.Context) ([]db.User, error) {
	return f.users, nil
}

type fakeSettings map[string][]byte

func (f fakeSettings) GetSetting(_ context.Context, key string) (db.Setting, error) {
	if v, ok := f[key]; ok {
		return db.Setting{Key: key, Value: v}, nil
	}
	return db.Setting{}, &errNotFound{}
}

type errNotFound struct{}

func (e *errNotFound) Error() string { return "not found" }

func activeUser(t time.Time, withChangedAt bool) db.User {
	u := db.User{
		ID:        pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		IsActive:  true,
		CreatedAt: pgtype.Timestamptz{Time: t, Valid: true},
	}
	if withChangedAt {
		u.PasswordChangedAt = pgtype.Timestamptz{Time: t, Valid: true}
	}
	return u
}

func inactiveUser() db.User {
	return db.User{
		ID:        pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		IsActive:  false,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-200 * 24 * time.Hour), Valid: true},
	}
}

func run(tw time.Time, users ...db.User) (*fakeNotifier, error) {
	notifier := &fakeNotifier{}
	eng := &PasswordExpiry{
		Queries:  &fakeQuerier{users: users},
		Settings: fakeSettings{},
		Notifier: notifier,
		Now:      func() time.Time { return tw },
	}
	err := eng.Run(context.Background())
	return notifier, err
}

func TestNoNotificationWhenPasswordFresh(t *testing.T) {
	// Changed 30 days ago, rotation 90d -> nowhere near expiry.
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-30*24*time.Hour), true))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 0 {
		t.Errorf("expected no notifications, got %v", notifier.events)
	}
}

func TestExpiringNotificationInWarningWindow(t *testing.T) {
	// Rotation 90d, warning 14d. Changed 80 days ago -> 10 days remain.
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-80*24*time.Hour), true))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expiring" {
		t.Errorf("expected password.expiring, got %v", notifier.events)
	}
}

func TestExpiringExactlyAtBoundary(t *testing.T) {
	// Changed exactly 90-14=76 days ago -> exactly at the warning window start.
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-(90-14)*24*time.Hour), true))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expiring" {
		t.Errorf("expected warning at boundary, got %v", notifier.events)
	}
}

func TestExpiredNotification(t *testing.T) {
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-91*24*time.Hour), true))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expired" {
		t.Errorf("expected password.expired, got %v", notifier.events)
	}
}

func TestExpiredExactlyAtBoundary(t *testing.T) {
	// Changed exactly 90 days ago -> expired at the boundary.
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-90*24*time.Hour), true))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expired" {
		t.Errorf("expected password.expired at boundary, got %v", notifier.events)
	}
}

func TestInactiveUsersSkipped(t *testing.T) {
	notifier, err := run(time.Now(), inactiveUser())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 0 {
		t.Errorf("inactive user must be skipped, got %v", notifier.events)
	}
}

func TestFallsBackToCreatedAt(t *testing.T) {
	// No password_changed_at; created 200 days ago -> expired by default policy.
	now := time.Now()
	notifier, err := run(now, activeUser(now.Add(-200*24*time.Hour), false))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expired" {
		t.Errorf("expected fallback expiry, got %v", notifier.events)
	}
}

func TestPolicyOverridesWindow(t *testing.T) {
	// Rotation 180 days, warning 30 days. Changed 170 days ago -> warned.
	settings := fakeSettings{
		policy.PasswordRotationPeriodKey: []byte(`180`),
		policy.PasswordWarningWindowKey:  []byte(`30`),
	}
	now := time.Now()
	notifier := &fakeNotifier{}
	eng := &PasswordExpiry{
		Queries:  &fakeQuerier{users: []db.User{activeUser(now.Add(-170*24*time.Hour), true)}},
		Settings: settings,
		Notifier: notifier,
		Now:      func() time.Time { return now },
	}
	if err := eng.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(notifier.events) != 1 || notifier.events[0] != "password.expiring" {
		t.Errorf("expected password.expiring, got %v", notifier.events)
	}
}
