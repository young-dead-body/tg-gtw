package cron

import (
	"context"
	"log"
	"time"

	"github.com/go-co-op/gocron/v2"
)

// StartPasswordExpiry schedules the password expiry scan and starts it.
// It returns the running scheduler; call Scheduler.Shutdown() to stop it.
func StartPasswordExpiry(logger *log.Logger, pe *PasswordExpiry, interval time.Duration) (gocron.Scheduler, error) {
	return Start(logger, "password expiry", func(ctx context.Context) error {
		return pe.Run(ctx)
	}, interval)
}

// StartExpiry schedules the access-deadline scan (access.expiring/expired).
func StartExpiry(logger *log.Logger, exp *Expiry, interval time.Duration) (gocron.Scheduler, error) {
	return Start(logger, "access expiry", func(ctx context.Context) error {
		return exp.Run(ctx)
	}, interval)
}

// StartCleanup schedules the periodic cleanup of expired sessions and stale
// registration requests (T16.2).
func StartCleanup(logger *log.Logger, c *Cleanup, interval time.Duration) (gocron.Scheduler, error) {
	return Start(logger, "cleanup", func(ctx context.Context) error {
		if _, err := c.Run(ctx); err != nil {
			return err
		}
		return nil
	}, interval)
}

// StartLogArchiver schedules the audit-log archival job (T16.3).
func StartLogArchiver(logger *log.Logger, a *LogArchiver, interval time.Duration) (gocron.Scheduler, error) {
	return Start(logger, "log archive", func(ctx context.Context) error {
		if _, err := a.Run(ctx); err != nil {
			return err
		}
		return nil
	}, interval)
}

// StartBackup schedules periodic pg_dump backups (T16.4).
func StartBackup(logger *log.Logger, b *Backup, interval time.Duration) (gocron.Scheduler, error) {
	return Start(logger, "database backup", func(ctx context.Context) error {
		if _, err := b.Run(ctx); err != nil {
			return err
		}
		return nil
	}, interval)
}

// Start runs a periodic job on a gocron scheduler started immediately.
func Start(logger *log.Logger, name string, run func(ctx context.Context) error, interval time.Duration) (gocron.Scheduler, error) {
	sched, err := gocron.NewScheduler()
	if err != nil {
		return nil, err
	}
	if _, err := sched.NewJob(
		gocron.DurationJob(interval),
		gocron.NewTask(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := run(ctx); err != nil {
				logger.Printf("cron %s: %v", name, err)
			}
		}),
	); err != nil {
		return nil, err
	}
	sched.Start()
	return sched, nil
}
