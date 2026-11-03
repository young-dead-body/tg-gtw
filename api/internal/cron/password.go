package cron

import (
	"context"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/policy"
)

// Notifier delivers business events. It is a subset of app.Notifier so the
// cron package does not depend on the HTTP layer.
type Notifier interface {
	Notify(ctx context.Context, event string, payload string) error
}

// Querier is the subset of db.Querier the password expiry job needs.
type Querier interface {
	ListUsers(ctx context.Context) ([]db.User, error)
}

// PasswordExpiry scans users whose password is expiring or already expired and
// emits warning events. `Now` is injectable to test boundary dates.
type PasswordExpiry struct {
	Queries  Querier
	Settings policy.SettingGetter
	Notifier Notifier
	Now      func() time.Time
}

// Run inspects every active user and emits notifications:
//   - password.expiring  — rotation window has started;
//   - password.expired   — the password is past its rotation deadline.
func (p *PasswordExpiry) Run(ctx context.Context) error {
	users, err := p.Queries.ListUsers(ctx)
	if err != nil {
		return err
	}
	pol, err := policy.LoadPassword(ctx, p.Settings)
	if err != nil {
		return err
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}

	for _, u := range users {
		if !u.IsActive {
			continue
		}
		changedAt := u.PasswordChangedAt
		if !changedAt.Valid {
			changedAt = u.CreatedAt
		}
		if !changedAt.Valid {
			continue
		}
		expiry := changedAt.Time.Add(pol.RotationPeriod)

		switch {
		case !expiry.After(now):
			_ = p.Notifier.Notify(ctx, "password.expired", u.ID.String())
		case !now.Add(pol.WarningWindow).Before(expiry):
			_ = p.Notifier.Notify(ctx, "password.expiring", u.ID.String())
		}
	}
	return nil
}
