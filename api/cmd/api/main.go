package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/company/telegram-gateway/api/internal/app"
	"github.com/company/telegram-gateway/api/internal/config"
	"github.com/company/telegram-gateway/api/internal/cron"
	"github.com/company/telegram-gateway/api/internal/database"
	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/feature"
	"github.com/company/telegram-gateway/api/internal/geo"
	"github.com/company/telegram-gateway/api/internal/logging"
	"github.com/company/telegram-gateway/api/internal/metrics"
	"github.com/company/telegram-gateway/api/internal/notify"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/ratelimit"
	"github.com/company/telegram-gateway/api/internal/session"
)

func main() {
	cfg := config.Load()

	stdLogger, err := logging.New(getenv("LOG_LEVEL", "info"), getenv("LOG_FILE", ""))
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer func() { _ = stdLogger.Sync() }()
	stdLog := logging.NewStdLogger(stdLogger)

	if flag := os.Getenv("RUN_MIGRATIONS"); flag == "1" || flag == "true" || flag == "up" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// MIGRATE_EXPECT_DB: обязательное имя БД для миграций (prod/stage
		// изоляция). Если не задано — миграции не запускаются.
		expectedDB := os.Getenv("MIGRATE_EXPECT_DB")
		if expectedDB == "" {
			log.Fatalf("migrations: MIGRATE_EXPECT_DB не задан — миграции отменены (защита от кросс-влияния прод/stage)")
		}
		if err := database.Migrate(ctx, cfg.PostgresDSN, expectedDB); err != nil {
			log.Fatalf("migrations: %v", err)
		}
		log.Println("migrations applied")
		return
	}
	if flag := os.Getenv("RUN_MIGRATIONS"); flag == "down" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := database.MigrateDown(ctx, cfg.PostgresDSN, os.Getenv("MIGRATE_EXPECT_DB")); err != nil {
			log.Fatalf("migrations down: %v", err)
		}
		log.Println("migrations rolled back")
		return
	}

	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// Generate and print credentials only when the first administrator is created.
	if err := bootstrapAdmin(ctx, pool, password.New(), os.Stdout); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	rdb, err := database.NewRedis(ctx, cfg.RedisAddr)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	// Business events are delivered to the Telegram bot (S11) over Redis
	// pub/sub. The same notifier backs both the HTTP handlers and the cron
	// jobs so password/access changes reach the bot exactly once per event.
	// When the tg-bot feature is disabled the notifier becomes a no-op so no
	// events are published (the Redis client is still used for sessions etc.).
	var notifier app.Notifier = notify.New(rdb)

	// Feature toggles (S11): host env seeds the default into the settings
	// table on startup; afterwards the admin can flip it at runtime via
	// /settings. The telegram.bot toggle controls the whole tg-bot stack.
	featureManager := feature.New(db.New(pool), "TG_BOT_ENABLED", true, 30*time.Second)
	if err := featureManager.Seed(context.Background(), map[string]bool{
		feature.TelegramBot: featureManager.EnabledOr(context.Background(), feature.TelegramBot, true),
	}); err != nil {
		log.Printf("feature: seed settings: %v", err)
	}
	tgBotEnabled := featureManager.EnabledOr(context.Background(), feature.TelegramBot, true)
	if !tgBotEnabled {
		notifier = app.NewNoopNotifier()
		stdLogger.Info("telegram bot disabled via feature toggle")
	}

	// Geo enrichment for login statistics (S12): optional MaxMind database.
	// Without GEOIP_DB the geo fields stay empty and nothing fails.
	var geoResolver geo.Resolver = geo.Noop{}
	if mmdbPath := os.Getenv("GEOIP_DB"); mmdbPath != "" {
		mmdb, err := geo.OpenMMDB(mmdbPath)
		if err != nil {
			stdLogger.Warn("geoip load failed", zap.Error(err))
		} else {
			defer func() { _ = mmdb.Close() }()
			geoResolver = mmdb
		}
	}

	// Rate limiting and brute-force lockout (T15.1, T15.2). Counters live in
	// Redis so all API instances share the same limits. A limit/env value of 0
	// disables the corresponding guard.
	rlStore := ratelimit.NewRedisStore(rdb)
	limiter := func(prefix string, max int, window time.Duration) *ratelimit.Limiter {
		if max <= 0 || window <= 0 {
			return nil
		}
		return ratelimit.NewLimiter(rlStore, prefix, int64(max), window)
	}
	var lockout *ratelimit.Lockout
	if threshold := envInt("LOCKOUT_THRESHOLD", 5); threshold > 0 {
		lockout = ratelimit.NewLockout(rlStore, int64(threshold), envDuration("LOCKOUT_WINDOW", 15*time.Minute))
	}

	// Prometheus metrics (S17, T17.2): request counters + dependency gauges.
	// The registry is exposed on a dedicated port (default 9091) so the fiber
	// API port stays free of monitoring traffic.
	metricSet := metrics.New()
	metricsAddr := getenv("METRICS_ADDR", ":9091")
	metricsSrv := &http.Server{
		Addr:              metricsAddr,
		Handler:           metricSet.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		stdLogger.Info("metrics listening", zap.String("addr", metricsAddr))
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			stdLogger.Error("metrics server", zap.Error(err))
		}
	}()
	defer func() { _ = metricsSrv.Shutdown(context.Background()) }()

	// Dependency gauges refresh every 15s (aligned with Prometheus scrape
	// interval).
	go func() {
		for range time.Tick(15 * time.Second) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			metricSet.SetPostgresUp(pool.Ping(ctx) == nil)
			metricSet.SetRedisUp(rdbPing(rdb)(ctx) == nil)
			metricSet.SetProxyUp(proxyReachable(ctx, getenv("TELEGRAM_PROXY_ADDR", "telegram-proxy:8080")))
			cancel()
		}
	}()

	application := app.New(&app.Server{
		Queries:          db.New(pool),
		Sessions:         session.NewRedisStore(rdb),
		Hasher:           password.New(),
		Notifier:         notifier,
		Geo:              geoResolver,
		RateAPI:          limiter("rl:api:", envInt("RATE_LIMIT_API_MAX", 120), envDuration("RATE_LIMIT_API_WINDOW", time.Minute)),
		RateLogin:        limiter("rl:login:", envInt("RATE_LIMIT_LOGIN_MAX", 10), envDuration("RATE_LIMIT_LOGIN_WINDOW", time.Minute)),
		RateRegistration: limiter("rl:registration:", envInt("RATE_LIMIT_REGISTRATION_MAX", 5), envDuration("RATE_LIMIT_REGISTRATION_WINDOW", time.Minute)),
		Lockout:          lockout,
		Metrics:          metricSet,
		Logger:           stdLogger,
		Config: app.Config{
			SessionTTL:           24 * time.Hour,
			PasswordReuseHistory: 5,
			BotToken:             os.Getenv("BOT_TOKEN"),
			BotEnabled:           tgBotEnabled,
			GatewayURL:           getenv("GATEWAY_URL", "https://localhost"),
		},
	}, app.HealthCheckers{
		Postgres: pool.Ping,
		Redis:    rdbPing(rdb),
	})

	// Password rotation scan (ТЗ §17): daily job warning about expiring and
	// expired passwords.
	cronInterval := 24 * time.Hour
	if v := os.Getenv("PASSWORD_EXPIRY_CRON_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cronInterval = d
		}
	}
	scheduler, err := cron.StartPasswordExpiry(stdLog, &cron.PasswordExpiry{
		Queries:  db.New(pool),
		Settings: db.New(pool),
		Notifier: notifier,
	}, cronInterval)
	if err != nil {
		log.Fatalf("start cron: %v", err)
	}
	defer func() { _ = scheduler.Shutdown() }()

	// Access-expiry scan (ТЗ §9): emits access.expiring / access.expired so the
	// bot can warn users before their access to Telegram Web ends.
	accessInterval := 6 * time.Hour
	if v := os.Getenv("ACCESS_EXPIRY_CRON_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			accessInterval = d
		}
	}
	accessScheduler, err := cron.StartExpiry(stdLog, &cron.Expiry{
		Queries:    db.New(pool),
		Notifier:   notifier,
		WarnBefore: 14 * 24 * time.Hour,
	}, accessInterval)
	if err != nil {
		log.Fatalf("start access cron: %v", err)
	}
	defer func() { _ = accessScheduler.Shutdown() }()

	// T16.2 — periodic cleanup of expired sessions and stale registration
	// requests. Daily by default (CLEANUP_CRON_INTERVAL).
	cleanupInterval := 24 * time.Hour
	if v := os.Getenv("CLEANUP_CRON_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cleanupInterval = d
		}
	}
	cleanupScheduler, err := cron.StartCleanup(stdLog, &cron.Cleanup{
		Queries:    db.New(pool),
		RequestAge: envDuration("CLEANUP_REQUEST_AGE", 30*24*time.Hour),
	}, cleanupInterval)
	if err != nil {
		log.Fatalf("start cleanup cron: %v", err)
	}
	defer func() { _ = cleanupScheduler.Shutdown() }()

	// T16.3 — archive audit-log rows older than the retention window
	// (ARCHIVE_LOG_AGE) into audit_log_archive.
	archiveInterval := 24 * time.Hour
	if v := os.Getenv("ARCHIVE_CRON_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			archiveInterval = d
		}
	}
	archiveScheduler, err := cron.StartLogArchiver(stdLog, &cron.LogArchiver{
		Queries: db.New(pool),
		Age:     envDuration("ARCHIVE_LOG_AGE", 90*24*time.Hour),
	}, archiveInterval)
	if err != nil {
		log.Fatalf("start archive cron: %v", err)
	}
	defer func() { _ = archiveScheduler.Shutdown() }()

	// T16.4 — periodic pg_dump backups kept in BACKUP_DIR (KEEP latest).
	backupInterval := 24 * time.Hour
	if v := os.Getenv("BACKUP_CRON_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			backupInterval = d
		}
	}
	backupJob := &cron.Backup{
		Logger:   stdLog,
		OnResult: metricSet.RecordBackup,
		Upload:   cron.ResticUploadFromEnv(),
		DSN:      cfg.PostgresDSN,
		Dir:      getenv("BACKUP_DIR", "/backups"),
		Keep:     envInt("BACKUP_KEEP", 7),
	}
	if last := backupJob.LastSuccess(); !last.IsZero() {
		metricSet.RecordBackup(last, true)
	}
	backupScheduler, err := cron.StartBackup(stdLog, backupJob, backupInterval)
	if err != nil {
		log.Fatalf("start backup cron: %v", err)
	}
	defer func() { _ = backupScheduler.Shutdown() }()

	addr := fmt.Sprintf(":%s", cfg.Port)
	stdLogger.Info("api listening", zap.String("addr", addr))
	if err := application.Listen(addr); err != nil {
		log.Fatal(err)
	}
}

func rdbPing(rdb *redis.Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	}
}

// proxyReachable reports whether the Telegram proxy backend accepts TCP
// connections. Used to feed the telegram_proxy_up gauge (T17.2).
func proxyReachable(ctx context.Context, addr string) bool {
	if addr == "" {
		return false
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
