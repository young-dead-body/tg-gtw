package cron

import (
	"context"
	"time"
)

// CleanupStore is the subset of db.Querier the cleanup job needs.
type CleanupStore interface {
	DeleteExpiredSessions(ctx context.Context) (int64, error)
	DeleteOldRegistrationRequests(ctx context.Context, secs float64) (int64, error)
}

// Cleanup removes stale rows from the database (ТЗ §17, T16.2):
//   - expired sessions (sessions.expires_at in the past);
//   - stale registration requests left in pending/rejected beyond RequestAge.
type Cleanup struct {
	Queries    CleanupStore
	RequestAge time.Duration
}

// Run performs the cleanup and returns the total number of removed rows.
func (c *Cleanup) Run(ctx context.Context) (int64, error) {
	if c.RequestAge <= 0 {
		c.RequestAge = 30 * 24 * time.Hour
	}

	n, err := c.Queries.DeleteExpiredSessions(ctx)
	if err != nil {
		return n, err
	}

	if tokens, ok := c.Queries.(interface{ DeleteExpiredTokens(context.Context) error }); ok {
		if err := tokens.DeleteExpiredTokens(ctx); err != nil {
			return n, err
		}
	}
	m, err := c.Queries.DeleteOldRegistrationRequests(ctx, c.RequestAge.Seconds())
	return n + m, err
}
