package app

import (
	"github.com/company/telegram-gateway/api/internal/session"
	"github.com/gofiber/fiber/v2"
)

func (s *Server) handleListSessions(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.ErrUnauthorized
	}
	manager, ok := s.Sessions.(session.Manager)
	if !ok {
		return fiber.ErrServiceUnavailable
	}
	rows, err := manager.List(c.Context(), u.ID.String())
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	for i := range rows {
		rows[i].Current = rows[i].ID == session.PublicID(c.Cookies(CookieName))
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"sessions": rows})
}
func (s *Server) handleDeleteSession(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.ErrUnauthorized
	}
	manager, ok := s.Sessions.(session.Manager)
	if !ok {
		return fiber.ErrServiceUnavailable
	}
	removed, err := manager.DeleteOwned(c.Context(), u.ID.String(), c.Params("id"))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if !removed {
		return fiber.ErrNotFound
	}
	if c.Params("id") == session.PublicID(c.Cookies(CookieName)) {
		s.clearSessionCookie(c)
	}
	s.recordAudit(c, u.ID, "session.revoked", "user", u.ID.String(), nil)
	return c.JSON(fiber.Map{"revoked": true})
}
func (s *Server) handleRevokeSessions(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.ErrUnauthorized
	}
	if err := s.Sessions.DeleteUser(c.Context(), u.ID.String()); err != nil {
		return fiber.ErrServiceUnavailable
	}
	s.clearSessionCookie(c)
	s.recordAudit(c, u.ID, "sessions.revoked", "user", u.ID.String(), nil)
	return c.JSON(fiber.Map{"revoked": true})
}
