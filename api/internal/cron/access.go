package cron

import (
	"context"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
)

// Expiry reports active users whose access deadline (expires_at) is near or
// past and emits events consumed by the Telegram bot (ТЗ §9, §12):
//   - access.expiring — access ends within the warning window;
//   - access.expired  — access deadline has passed.
type Expiry struct {
	Queries  QuerierWithExpiry
	Notifier Notifier
	// WarnBefore is the window preceding the deadline that triggers a warning.
	WarnBefore time.Duration
	Now        func() time.Time
}

// QuerierWithExpiry is the subset of db.Querier the expiry job needs.
type QuerierWithExpiry interface {
	ListUsersWithExpiry(ctx context.Context) ([]db.User, error)
}

// Run scans users with a configured access deadline and emits expiry events.
func (e *Expiry) Run(ctx context.Context) error {
	if e.WarnBefore <= 0 {
		e.WarnBefore = 14 * 24 * time.Hour
	}
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	users, err := e.Queries.ListUsersWithExpiry(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.ExpiresAt.Valid {
			continue
		}
		expiry := u.ExpiresAt.Time
		switch {
		case !expiry.After(now):
			_ = e.Notifier.Notify(ctx, "access.expired", u.ID.String())
		case !now.Add(e.WarnBefore).Before(expiry):
			_ = e.Notifier.Notify(ctx, "access.expiring", u.ID.String())
		}
	}
	return nil
}
