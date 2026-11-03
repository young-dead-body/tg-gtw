package cron

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ArchiveStore is the subset of db.Querier the log-archive job needs.
type ArchiveStore interface {
	CountOldAuditLogs(ctx context.Context, createdAt pgtype.Timestamptz) (int64, error)
	ArchiveOldAuditLogs(ctx context.Context, createdAt pgtype.Timestamptz) error
}

// LogArchiver moves audit-log rows older than Age from audit_log into the
// audit_log_archive table in one atomic statement (ТЗ §17, T16.3).
type LogArchiver struct {
	Queries ArchiveStore
	Age     time.Duration
	Now     func() time.Time
}

// Run archives audit-log entries older than the retention window and reports
// how many rows were moved.
func (a *LogArchiver) Run(ctx context.Context) (int64, error) {
	if a.Age <= 0 {
		a.Age = 90 * 24 * time.Hour
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	cutoff := pgtype.Timestamptz{Time: now.Add(-a.Age), Valid: true}

	count, err := a.Queries.CountOldAuditLogs(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if err := a.Queries.ArchiveOldAuditLogs(ctx, cutoff); err != nil {
		return 0, err
	}
	return count, nil
}
