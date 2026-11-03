package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"
)

// Bot API — private endpoints called by the Telegram notification bot (S11).
// They are guarded by a shared service token (Authorization: Bearer <token>),
// never by user sessions, and expose exactly what the bot needs: credential
// issuance and user resolution for notifications.

const (
	// BotAPIEventPasswordChanged is emitted after credentials are re-issued.
	BotAPIEventPasswordChanged = "password.changed"
	// BotAPIEventPasswordIssued is emitted when the bot mints a new password.
	BotAPIEventPasswordIssued = "password.issued"
)

type botCredentialsRequest struct {
	TelegramID int64 `json:"telegram_user_id"`
}

type botCredentialsResponse struct {
	ResetURL  string `json:"reset_url"`
	ExpiresIn int    `json:"expires_in"`
}

// requireBot authenticates the bot service via a constant-time bearer-token
// comparison. 401 when the header is missing or the token mismatches.
func (s *Server) requireBot() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.Config.BotToken == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "bot service not configured"})
		}
		auth := c.Get(fiber.HeaderAuthorization)
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || token == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid bot token"})
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.Config.BotToken)) != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid bot token"})
		}
		return c.Next()
	}
}

// handleBotCredentials issues a single-use password reset link for a verified
// Telegram sender ID. The password is chosen only on the gateway reset page.
func (s *Server) handleBotCredentials(c *fiber.Ctx) error {
	var req botCredentialsRequest
	if c.BodyParser(&req) != nil || req.TelegramID <= 0 {
		return fiber.ErrBadRequest
	}
	u, err := s.Queries.GetUserByTelegramID(c.Context(), req.TelegramID)
	if err != nil {
		return fiber.ErrNotFound
	}
	if !accountEnabled(u) {
		return fiber.ErrForbidden
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fiber.ErrServiceUnavailable
	}
	token := hex.EncodeToString(random[:])
	issued, err := s.Queries.IssuePasswordResetToken(c.Context(), db.IssuePasswordResetTokenParams{
		TelegramUserID: req.TelegramID, TokenHash: tokenHash(token), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(15 * time.Minute), Valid: true},
	})
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	link, err := s.resetURL(token)
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	c.Set("Cache-Control", "no-store")
	s.recordAudit(c, u.ID, "password.reset_requested", "user", issued.UserID.String(), nil)
	return c.JSON(botCredentialsResponse{ResetURL: link, ExpiresIn: 900})
}

// handleBotUser resolves a user by id for the bot's notification flow
// (delivering "access expired" / "password changed" messages to the right
// Telegram handle).
func (s *Server) handleBotUser(c *fiber.Ctx) error {
	id, err := uuidFromString(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user id"})
	}
	u, err := s.Queries.GetUserByID(c.Context(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
	}
	telegram := ""
	if u.Telegram.Valid {
		telegram = u.Telegram.String
	}
	expiresAt := (*time.Time)(nil)
	if u.ExpiresAt.Valid {
		t := u.ExpiresAt.Time
		expiresAt = &t
	}
	var telegramID int64
	if binding, err := s.Queries.GetTelegramBinding(c.Context(), id); err == nil {
		telegramID = binding.TelegramUserID
	}
	return c.JSON(fiber.Map{
		"telegram_user_id": telegramID,
		"id":               u.ID.String(),
		"email":            u.Email,
		"first_name":       u.FirstName,
		"last_name":        u.LastName,
		"telegram":         telegram,
		"is_active":        u.IsActive,
		"expires_at":       expiresAt,
	})
}
