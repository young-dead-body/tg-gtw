package cron

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// DumpFn creates a logical database dump. It is injectable so tests can avoid
// shelling out to a real pg_dump.
type DumpFn func(ctx context.Context, dsn, file string) error

// DefaultDump runs pg_dump against the given DSN and writes the SQL dump to
// file. pg_dump must be available on PATH (postgresql-client package).
func DefaultDump(ctx context.Context, dsn, file string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
		return fmt.Errorf("backup requires a PostgreSQL URL")
	}
	password, _ := u.User.Password()
	query := u.Query()
	if value := query.Get("password"); value != "" {
		password = value
		query.Del("password")
		u.RawQuery = query.Encode()
	}
	u.User = url.User(u.User.Username())
	cmd := exec.CommandContext(ctx, "pg_dump", "--no-owner", "--no-acl", "--dbname="+u.String(), "--file="+file)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump failed: %w", err)
	}

	return nil
}

// Backup creates periodic pg_dump backups of the database (ТЗ §17, T16.4)
// and keeps only the Keep most recent dumps in Dir.
type Backup struct {
	Logger   *log.Logger
	DSN      string
	Dir      string
	Keep     int
	Now      func() time.Time
	Dump     DumpFn
	OnResult func(time.Time, bool)
	Upload   func(context.Context, string) error
}

// fileName returns the backup file path for a given timestamp.
func (b *Backup) fileName(t time.Time) string {
	return filepath.Join(b.Dir, "backup_"+t.UTC().Format("20060102T150405Z")+".sql")
}

// Run takes a single backup and prunes old dumps. It returns the path written.
func (b *Backup) Run(ctx context.Context) (string, error) {
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	path := b.fileName(now)
	if err := b.dump(ctx, path); err != nil {
		if b.OnResult != nil {
			b.OnResult(time.Time{}, false)
		}
		return "", err
	}
	if b.Upload != nil {
		if err := b.Upload(ctx, path); err != nil {
			if b.OnResult != nil {
				b.OnResult(time.Time{}, false)
			}
			return path, err
		}
	}

	if err := b.saveSuccess(now); err != nil {
		if b.OnResult != nil {
			b.OnResult(time.Time{}, false)
		}
		return path, err
	}
	if b.OnResult != nil {
		b.OnResult(now, true)
	}
	if err := b.prune(); err != nil {
		b.warn("prune backups: %v", err)
	}
	return path, nil
}

func (b *Backup) dump(ctx context.Context, path string) error {
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(b.Dir, 0o700); err != nil {
		return err
	}
	if b.Dump == nil {
		b.Dump = DefaultDump
	}
	temp, err := os.CreateTemp(b.Dir, ".backup-*.partial")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Close(); err != nil {
		return err
	}
	if err := b.Dump(ctx, b.DSN, tempPath); err != nil {
		return err
	}
	file, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if info.Size() == 0 {
		return fmt.Errorf("empty database backup")
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	dir, err := os.Open(b.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return err
	}

	b.warn("backup written: %s", path)
	return nil
}

// prune removes the oldest dumps beyond b.Keep (zero or negative Keep retains all).
func (b *Backup) prune() error {
	if b.Keep <= 0 {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(b.Dir, "backup_*.sql"))
	if err != nil {
		return err
	}
	if len(matches) <= b.Keep {
		return nil
	}
	sort.Strings(matches)
	for _, f := range matches[:len(matches)-b.Keep] {
		if err := os.Remove(f); err != nil {
			return err
		}
		b.warn("pruned backup: %s", f)
	}
	return nil
}

func (b *Backup) warn(format string, args ...any) {
	if b.Logger != nil {
		b.Logger.Printf(format, args...)
	}
}

// Restic encrypts backups before sending them to an explicitly configured
// repository. No repository means local backups only.
func ResticUploadFromEnv() func(context.Context, string) error {
	if os.Getenv("RESTIC_REPOSITORY") == "" {
		return nil
	}
	return func(ctx context.Context, path string) error {
		cmd := exec.CommandContext(ctx, "restic", "backup", "--read-concurrency", "1", "--tag", "telegram-gateway", path)
		cmd.Env = append(os.Environ(), "RESTIC_CACHE_DIR="+filepath.Join(filepath.Dir(path), ".restic-cache"))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("encrypted offsite backup failed: %w", err)
		}
		return nil
	}
}

func (b *Backup) LastSuccess() time.Time {
	data, err := os.ReadFile(filepath.Join(b.Dir, ".last-success"))
	if err != nil {
		return time.Time{}
	}
	unix, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
func (b *Backup) saveSuccess(at time.Time) error {
	temp, err := os.CreateTemp(b.Dir, ".success-*.partial")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.WriteString(strconv.FormatInt(at.Unix(), 10)); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(b.Dir, ".last-success"))
}
