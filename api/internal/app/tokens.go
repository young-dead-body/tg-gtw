package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
func (s *Server) issueToken(c *fiber.Ctx, uid pgtype.UUID, purpose string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	err := s.Queries.IssueOneTimeToken(c.Context(), db.IssueOneTimeTokenParams{
		UserID: uid, Purpose: purpose, TokenHash: tokenHash(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(15 * time.Minute), Valid: true},
	})
	return token, err
}
func accountEnabled(u db.User) bool {
	return u.IsActive && !u.BlockedAt.Valid && (!u.ExpiresAt.Valid || u.ExpiresAt.Time.After(time.Now()))
}
func (s *Server) handleTelegramLink(c *fiber.Ctx) error {
	if !s.Config.BotEnabled {
		return fiber.ErrNotFound
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.ErrUnauthorized
	}
	if id := c.Params("id"); id != "" {
		uid, err := uuidFromString(id)
		if err != nil {
			return fiber.ErrBadRequest
		}
		u, err = s.Queries.GetUserByID(c.Context(), uid)
		if err != nil {
			return fiber.ErrNotFound
		}
	}
	if !accountEnabled(u) {
		return fiber.ErrForbidden
	}
	token, err := s.issueToken(c, u.ID, "telegram_link")
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	c.Set("Cache-Control", "no-store")
	s.recordAudit(c, adminID(c), "telegram.link_issued", "user", u.ID.String(), nil)
	return c.JSON(fiber.Map{"code": token, "expires_in": 900})
}
func (s *Server) handleBotLink(c *fiber.Ctx) error {
	var req struct {
		Code       string `json:"code"`
		TelegramID int64  `json:"telegram_user_id"`
	}
	if c.BodyParser(&req) != nil || len(req.Code) != 64 || req.TelegramID <= 0 {
		return fiber.ErrBadRequest
	}
	binding, err := s.Queries.BindTelegram(c.Context(), db.BindTelegramParams{TokenHash: tokenHash(req.Code), TelegramUserID: req.TelegramID})
	if err != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "invalid, expired or already linked code"})
	}
	s.recordAudit(c, binding.UserID, "telegram.linked", "user", binding.UserID.String(), nil)
	return c.JSON(fiber.Map{"linked": true})
}
func (s *Server) handlePasswordReset(c *fiber.Ctx) error {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if c.BodyParser(&req) != nil || len(req.Token) != 64 || len(req.Password) < 8 || len(req.Password) > 1024 {
		return fiber.ErrBadRequest
	}
	digest := tokenHash(req.Token)
	u, err := s.Queries.GetPasswordResetUser(c.Context(), digest)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid or expired reset link"})
	}
	reused, err := s.isPasswordReused(c.Context(), u, req.Password)
	if err != nil {
		return passwordError(c, err)
	}
	if reused {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "password was used recently"})
	}
	hash, err := s.Hasher.Hash(req.Password)
	if err != nil {
		return passwordError(c, err)
	}

	if err := s.Sessions.DeleteUser(c.Context(), u.ID.String()); err != nil {
		return fiber.ErrServiceUnavailable
	}
	changed, err := s.Queries.RedeemPasswordReset(c.Context(), db.RedeemPasswordResetParams{TokenHash: digest, PasswordHash: pgtype.Text{String: hash, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return fiber.ErrBadRequest
	}
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	s.clearSessionCookie(c)
	s.recordAudit(c, changed.ID, auditPassword, "user", changed.ID.String(), map[string]any{"channel": "reset_link"})
	if s.Notifier != nil {
		_ = s.Notifier.Notify(c.Context(), BotAPIEventPasswordChanged, changed.ID.String())
	}
	return c.JSON(fiber.Map{"message": "password changed"})
}
func (s *Server) resetURL(token string) (string, error) {
	u, err := url.Parse(s.Config.GatewayURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("invalid gateway URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawQuery = ""
	u.Fragment = "reset=" + token
	return u.String(), nil
}
