package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"

	"github.com/gofiber/fiber/v2"

	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/ratelimit"
)

// CSRF (T15.3): double-submit pattern. The server issues a tg_csrf cookie on
// the first GET; state-changing requests must echo it in X-CSRF-Token. The
// cookie is SameSite=Strict and Secure, and the constant-time comparison
// prevents a cross-site attacker from forging the header.

// CSRFCookieName is the double-submit token cookie.
const CSRFCookieName = "tg_csrf"

// csrfTokenLengthBytes is the entropy of the issued token.
const csrfTokenLengthBytes = 32

// ensureCSRFCookie issues a token cookie when the request has none. Applied
// globally so anonymous clients (consent, login) get a token before their
// first POST.
func ensureCSRFCookie(c *fiber.Ctx) error {
	if c.Cookies(CSRFCookieName) == "" {
		c.Cookie(&fiber.Cookie{
			Name:     CSRFCookieName,
			Value:    newCSRFToken(),
			Path:     "/",
			Secure:   true,
			SameSite: "Strict",
		})
	}
	return c.Next()
}

// requireCSRF rejects state-changing requests without a valid double-submit
// token. Safe methods pass through.
func requireCSRF(c *fiber.Ctx) error {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return c.Next()
	}
	token := c.Cookies(CSRFCookieName)
	if token == "" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "csrf token missing"})
	}
	header := c.Get("X-CSRF-Token")
	if header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(token)) != 1 {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "csrf token mismatch"})
	}
	return c.Next()
}

// newCSRFToken returns a cryptographically random hex token.
func newCSRFToken() string {
	buf := make([]byte, csrfTokenLengthBytes)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("csrf: rand: %v", err)
	}
	return hex.EncodeToString(buf)
}

// rateLimit wraps a limiter into a Fiber middleware. A nil limiter disables
// the guard. Redis/limit failures fail closed with 500 so misconfigurations
// are visible instead of silently open.
func rateLimit(l *ratelimit.Limiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if l == nil {
			return c.Next()
		}
		allowed, err := l.Allow(c.Context(), c.IP())
		if err != nil {
			log.Printf("ratelimit: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
		}
		if !allowed {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "too many requests, try later"})
		}
		return c.Next()
	}
}

func passwordBusy(c *fiber.Ctx) error {
	c.Set("Retry-After", "2")
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": password.ErrBusy.Error()})
}
func passwordError(c *fiber.Ctx, err error) error {
	if errors.Is(err, password.ErrBusy) {
		return passwordBusy(c)
	}
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
}
