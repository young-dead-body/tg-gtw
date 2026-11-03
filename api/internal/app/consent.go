package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/netip"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

const consentDocumentVersionKey = "consent.document_version"

const defaultConsentDocumentVersion = "1.0"

const consentReceiptCookie = "tg_consent_receipt"

// handleConsentInfo is public metadata about the consent policy (ТЗ §11).
func (s *Server) handleConsentInfo(c *fiber.Ctx) error {
	version, err := s.consentDocumentVersion(c.Context())
	if err != nil {
		log.Printf("consent: document version: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	return c.JSON(fiber.Map{
		"document_version": version,
		"required":         true,
	})
}

type consentRequest struct {
	Accepted        bool   `json:"accepted"`
	DocumentVersion string `json:"document_version"`
}

// handleConsent records an accepted privacy policy: date, IP, User-Agent and
// document version. Consent is a precondition for the login form (ТЗ §11).
func (s *Server) handleConsent(c *fiber.Ctx) error {
	var req consentRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if !req.Accepted {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "consent must be accepted"})
	}

	version, err := s.consentDocumentVersion(c.Context())
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "consent policy unavailable"})
	}
	if req.DocumentVersion != "" && req.DocumentVersion != version {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "consent_version_changed", "document_version": version})
	}

	// If the visitor already has a session, the consent is attributed to them.
	var userID pgtype.UUID
	if u, err := s.authenticate(c); err == nil {
		userID = u.ID
	}

	ip, err := netip.ParseAddr(c.IP())
	if err != nil {
		ip = netip.IPv4Unspecified()
	}

	consent, err := s.Queries.InsertConsent(c.Context(), db.InsertConsentParams{
		UserID:          userID,
		DocumentVersion: version,
		Ip:              &ip,
		UserAgent:       pgtype.Text{String: c.Get(fiber.HeaderUserAgent), Valid: true},
	})
	if err != nil {
		log.Printf("consent: insert: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	if !userID.Valid {
		c.Cookie(&fiber.Cookie{Name: consentReceiptCookie, Value: consent.ID.String(), Path: "/", Secure: true, HTTPOnly: true, SameSite: "Strict", MaxAge: 86400})
	}

	s.recordAudit(c, userID, auditConsent, "consent", version, nil)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"document_version": consent.DocumentVersion,
		"accepted_at":      consent.AcceptedAt.Time,
	})
}

// consentDocumentVersion reads the configured policy version from settings.
func (s *Server) consentDocumentVersion(ctx context.Context) (string, error) {
	setting, err := s.Queries.GetSetting(ctx, consentDocumentVersionKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultConsentDocumentVersion, nil
	}
	if err != nil {
		return "", err
	}
	var raw string
	if err := json.Unmarshal(setting.Value, &raw); err != nil {
		return "", err
	}
	if raw == "" {
		return defaultConsentDocumentVersion, nil
	}
	return raw, nil
}
