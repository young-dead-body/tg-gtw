package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"github.com/company/telegram-gateway/api/migrations"
)

// Connect creates a pgx connection pool and verifies connectivity.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// NewRedis creates a Redis client and verifies connectivity.
func NewRedis(ctx context.Context, addr string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// Migrate applies the embedded SQL migrations to the configured database.
// expectedDB, when non-empty, must match the database the DSN points at,
// otherwise migration is refused (isolation guard between prod/stage).
func Migrate(ctx context.Context, dsn, expectedDB string) error {
	return runMigrations(ctx, dsn, expectedDB, func(m *migrate.Migrate) error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

// MigrateDown rolls back all applied migrations.
func MigrateDown(ctx context.Context, dsn, expectedDB string) error {
	return runMigrations(ctx, dsn, expectedDB, func(m *migrate.Migrate) error {
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

func runMigrations(ctx context.Context, dsn, expectedDB string, run func(*migrate.Migrate) error) error {
	if expectedDB != "" {
		got, err := currentDatabase(ctx, dsn)
		if err != nil {
			return fmt.Errorf("guard: current database: %w", err)
		}
		if got != expectedDB {
			return fmt.Errorf("guard: DSN points at database %q, expected %q — миграции не запущены (защита от кросс-влияния прод/stage)", got, expectedDB)
		}
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	pgConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn for migrate: %w", err)
	}
	conn := stdlib.OpenDB(*pgConfig)
	defer func() { _ = conn.Close() }()

	driver, err := migratepgx.WithInstance(conn, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}

	// Version guard: refuse to run when the database is already migrated
	// beyond what this binary knows. An old binary against a newer schema is
	// the exact failure mode that broke production (DB at v18, binary with 12
	// migrations); running would either skip or corrupt the history.
	maxVersion := maxEmbeddedVersion()
	currentVersion, dirty, err := m.Version()
	if err == nil && currentVersion > maxVersion {
		return fmt.Errorf("guard: database schema at version %d exceeds this binary's max migration %d — образ собран из другого контура/ветки, миграции не запущены", currentVersion, maxVersion)
	}
	if err != nil {
		// golang-migrate reports ErrNilVersion for a fresh database: fine.
		if !errors.Is(err, migrate.ErrNilVersion) {
			return fmt.Errorf("guard: read schema version: %w", err)
		}
	}
	if dirty {
		return fmt.Errorf("guard: schema_migrations dirty — ручное вмешательство требуется")
	}

	if err := run(m); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// currentDatabase returns the database name the DSN connects to.
func currentDatabase(ctx context.Context, dsn string) (string, error) {
	pgConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, pgConfig)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	var name string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// maxEmbeddedVersion returns the highest migration version embedded in the
// binary by scanning the migration filenames (000001_*.up.sql / .down.sql).
func maxEmbeddedVersion() uint {
	max := uint(0)
	_ = fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		var n uint
		if _, err := fmt.Sscanf(filepath.Base(path), "%06d", &n); err != nil {
			return nil
		}
		if n > max {
			max = n
		}
		return nil
	})
	return max
}
