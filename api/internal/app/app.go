package app

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/company/telegram-gateway/api/docs"
	"github.com/company/telegram-gateway/api/documents"
	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/geo"
	"github.com/company/telegram-gateway/api/internal/metrics"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/ratelimit"
	"github.com/company/telegram-gateway/api/internal/session"
)

// Config holds application-level settings.
type Config struct {
	// SessionTTL is how long a login session lives in Redis.
	SessionTTL time.Duration
	// PasswordReuseHistory is how many recent passwords cannot be reused.
	PasswordReuseHistory int
	// BotToken is the shared secret the Telegram bot presents as
	// Authorization: Bearer <token>. Empty disables the /bot endpoints.
	BotToken string
	// BotEnabled controls whether the /bot/* routes are registered at all
	// (feature toggle, default true). When false the bot endpoints are not
	// exposed and the notifier is a no-op.
	BotEnabled bool
	// GatewayURL is the external base URL reported to the bot when it issues
	// credentials (e.g. "https://localhost").
	GatewayURL string
}

// HealthCheckers performs liveness probes for external dependencies.
type HealthCheckers struct {
	// Postgres pings the database. Nil means the probe is skipped.
	Postgres func(ctx context.Context) error
	// Redis pings the cache. Nil means the probe is skipped.
	Redis func(ctx context.Context) error
}

// Server bundles the dependencies needed by the HTTP handlers.
type Server struct {
	// Queries is the generated data access layer.
	Queries db.Querier
	// Sessions stores server-side sessions.
	Sessions session.Store
	// Hasher hashes and verifies passwords (Argon2id).
	Hasher password.Hasher
	// Notifier delivers out-of-band events (admin/user notifications).
	Notifier Notifier
	// Geo resolves country/region/city from IP for login statistics (ТЗ §12).
	// Nil disables geo enrichment (fields stay empty).
	Geo geo.Resolver
	// RateAPI limits all requests per IP. Nil disables the guard.
	RateAPI *ratelimit.Limiter
	// RateLogin limits /login attempts per IP. Nil disables the guard.
	RateLogin *ratelimit.Limiter
	// RateRegistration limits /registration attempts per IP. Nil disables.
	RateRegistration *ratelimit.Limiter
	// Lockout blocks IPs and accounts after repeated failed logins (T15.2).
	// Nil disables brute-force protection.
	Lockout *ratelimit.Lockout
	// Metrics exposes Prometheus collectors (T17.2). Nil disables HTTP metrics.
	Metrics *metrics.Metrics
	// Logger is a structured zap logger (T17.4). Nil disables request logging.
	Logger *zap.Logger
	// Config carries tunables.
	Config Config
}

type checkResult struct {
	Status string `json:"status"`
}

// requestLogger logs every request with method, path, status and duration
// (structured zap output, T17.4).
func requestLogger(logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		dur := time.Since(start)
		fields := []zap.Field{
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", c.Response().StatusCode()),
			zap.Duration("duration", dur),
			zap.String("ip", c.IP()),
		}
		if err != nil {
			if fe, ok := err.(*fiber.Error); ok {
				fields = append(fields, zap.Int("status", fe.Code))
			}
		}
		logger.Info("http.request", fields...)
		return err
	}
}

const (
	// CookieName is the HttpOnly session cookie set on login.
	CookieName = session.CookieName

	// FeatureTelegramBot is the public feature flag key surfaced to the web UI.
	FeatureTelegramBot = "telegram_bot"
)

// handleFeatures returns the set of enabled feature flags (public read-only).
func (s *Server) handleFeatures(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		FeatureTelegramBot: s.Config.BotEnabled,
	})
}

// New builds the Fiber application with routes wired up.
// Separated from main to allow integration testing via app.Test().
func New(srv *Server, checkers HealthCheckers) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "telegram-gateway-api",
	})

	// Prometheus HTTP metrics (T17.2): registered before routes so every
	// request (including health) is measured.
	if srv.Metrics != nil {
		app.Use(srv.Metrics.FiberMiddleware)
	}
	// Structured request logging (T17.4).
	if srv.Logger != nil {
		app.Use(requestLogger(srv.Logger))
	}

	// OpenAPI contract (T17.1): raw 3.1 spec + a self-contained Swagger-style
	// UI that renders it. Public read-only endpoints.
	app.Get("/swagger.json", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).Type("json").Send(docs.OpenAPI)
	})
	app.Get("/swagger", func(c *fiber.Ctx) error {
		c.Type("html", "utf-8")
		return c.SendString(swaggerUIHTML)
	})
	app.Get("/swagger-ui.js", func(c *fiber.Ctx) error {
		c.Type("js", "utf-8")
		return c.SendString(swaggerUIJS)
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()

		checks := map[string]checkResult{}
		allOK := true

		if checkers.Postgres != nil {
			if err := checkers.Postgres(ctx); err != nil {
				allOK = false
				checks["postgres"] = checkResult{Status: "error"}
			} else {
				checks["postgres"] = checkResult{Status: "ok"}
			}
		}
		if checkers.Redis != nil {
			if err := checkers.Redis(ctx); err != nil {
				allOK = false
				checks["redis"] = checkResult{Status: "error"}
			} else {
				checks["redis"] = checkResult{Status: "ok"}
			}
		}

		status := "ok"
		code := fiber.StatusOK
		if !allOK {
			status = "degraded"
			code = fiber.StatusServiceUnavailable
		}

		return c.Status(code).JSON(fiber.Map{
			"status":  status,
			"service": "api",
			"checks":  checks,
		})
	})

	// Feature toggles (S11): public read-only so the web UI can hide the
	// tg-bot-related fields when the bot is disabled.
	app.Get("/features", srv.handleFeatures)
	app.Get("/documents/consent", func(c *fiber.Ctx) error { return c.Type("txt", "utf-8").Send(documents.Consent) })
	app.Get("/documents/privacy-policy", func(c *fiber.Ctx) error { return c.Type("txt", "utf-8").Send(documents.PrivacyPolicy) })

	// CSRF (T15.3): every request carries a double-submit token cookie; state
	// changes must echo it in X-CSRF-Token.
	app.Use(ensureCSRFCookie)
	app.Use(rateLimit(srv.RateAPI))

	app.Post("/login", rateLimit(srv.RateLogin), requireCSRF, srv.handleLogin)
	app.Post("/logout", srv.requireAuth(), requireCSRF, srv.handleLogout)
	app.Get("/me", srv.requireAuth(), srv.handleMe)
	app.Get("/me/sessions", srv.requireAuth(), srv.handleListSessions)
	app.Delete("/me/sessions/:id", srv.requireAuth(), requireCSRF, srv.handleDeleteSession)
	app.Post("/me/sessions/revoke", srv.requireAuth(), requireCSRF, srv.handleRevokeSessions)
	app.Post("/me/telegram-link", srv.requireAuth(), requireCSRF, srv.handleTelegramLink)
	app.Post("/me/password", srv.requireAuth(), requireCSRF, srv.handleChangePassword)
	app.Post("/password/reset", rateLimit(srv.RateLogin), requireCSRF, srv.handlePasswordReset)
	app.Post("/registration", rateLimit(srv.RateRegistration), requireCSRF, srv.handleRegistration)

	app.Get("/consent", srv.handleConsentInfo)
	app.Post("/consent", requireCSRF, srv.handleConsent)

	users := app.Group("/users", srv.requireAdmin(), requireCSRF)
	users.Get("/", srv.handleListUsers)
	users.Post("/", srv.handleCreateUser)
	users.Patch("/:id", srv.handleUpdateUser)
	users.Post("/:id/telegram-link", srv.handleTelegramLink)
	users.Delete("/:id", srv.handleDeleteUser)

	// Statistics (S12): admin-only access to login analytics (ТЗ §12, §15).
	app.Get("/stats", srv.requireAdmin(), srv.handleListStats)

	// Audit log (S13): admin-only access with filters and pagination (ТЗ §13).
	app.Get("/logs", srv.requireAdmin(), srv.handleListLogs)

	// Admin panel (S14): dashboard metrics, registration review, settings.
	app.Get("/dashboard", srv.requireAdmin(), srv.handleDashboard)
	requests := app.Group("/registration-requests", srv.requireAdmin(), requireCSRF)
	requests.Get("/", srv.handleListRegistrationRequests)
	requests.Patch("/:id", srv.handleUpdateRegistrationRequest)
	settings := app.Group("/settings", srv.requireAdmin(), requireCSRF)
	settings.Get("/:key", srv.handleGetSetting)
	settings.Put("/:key", srv.handlePutSetting)

	// Private bot API (S11): the Telegram bot authenticates with a shared
	// service token, never with a user session. Skipped entirely when the
	// tg-bot feature is disabled.
	if srv.Config.BotEnabled {
		bot := app.Group("/bot", srv.requireBot())
		bot.Post("/credentials", srv.handleBotCredentials)
		bot.Post("/link", srv.handleBotLink)
		bot.Get("/users/:id", srv.handleBotUser)
	}

	return app
}
