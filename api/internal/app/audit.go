package app

import (
	"encoding/json"
	"log"
	"net/netip"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// Audit actions (S13). Login actions are recorded from the statistics hook,
// everything else from the handlers themselves.
const (
	auditLoginSuccess         = "login.success"
	auditLoginFailed          = "login.failed"
	auditLogout               = "logout"
	auditPassword             = "password.changed"
	auditRegistration         = "registration.requested"
	auditRegistrationApproved = "registration.approved"
	auditRegistrationRejected = "registration.rejected"
	auditRegistrationApp      = "registration.approved"
	auditRegistrationRej      = "registration.rejected"
	auditUserCreated          = "user.created"
	auditUserUpdated          = "user.updated"
	auditUserDeleted          = "user.deleted"
	auditConsent              = "consent.accepted"
	auditCredentials          = "credentials.issued"
	auditSettings             = "settings.updated"
)

// recordAudit writes an audit_log row (ТЗ §13). The actor is optional
// (anonymous actions like consent acceptance have a NULL user_id). Failures
// are logged but never break the request flow.
func (s *Server) recordAudit(c *fiber.Ctx, actorID pgtype.UUID, action, entityType, entityID string, details map[string]any) {
	ip, err := netip.ParseAddr(c.IP())
	if err != nil {
		ip = netip.Addr{}
	}

	detailsJSON, err := json.Marshal(details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	if _, err := s.Queries.InsertAuditLog(c.Context(), db.InsertAuditLogParams{
		UserID:     actorID,
		Action:     action,
		EntityType: pgtext(entityType),
		EntityID:   pgtext(entityID),
		Ip:         ipPtr(ip),
		UserAgent:  pgtext(c.Get(fiber.HeaderUserAgent)),
		Details:    detailsJSON,
	}); err != nil {
		log.Printf("audit: %v", err)
	}
}

// adminID returns the authenticated admin's ID from the request locals, or an
// invalid UUID when absent.
func adminID(c *fiber.Ctx) pgtype.UUID {
	u, ok := c.Locals(userLocalsKey).(db.User)
	if !ok {
		return pgtype.UUID{}
	}
	return u.ID
}

type auditItem struct {
	ID         int64          `json:"id"`
	UserID     *string        `json:"user_id"`
	Action     string         `json:"action"`
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	IP         string         `json:"ip"`
	UserAgent  string         `json:"user_agent"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toAuditItem(a db.AuditLog) auditItem {
	item := auditItem{
		ID:         a.ID,
		Action:     a.Action,
		EntityType: a.EntityType.String,
		EntityID:   a.EntityID.String,
		UserAgent:  a.UserAgent.String,
		CreatedAt:  a.CreatedAt.Time,
	}
	if a.UserID.Valid {
		uid := a.UserID.String()
		item.UserID = &uid
	}
	if a.Ip != nil {
		item.IP = a.Ip.String()
	}
	if len(a.Details) > 0 {
		var m map[string]any
		if err := json.Unmarshal(a.Details, &m); err == nil {
			item.Details = m
		}
	}
	if item.Details == nil {
		item.Details = map[string]any{}
	}
	return item
}

// handleListLogs is the admin endpoint GET /logs (ТЗ §13.3) with filters on
// actor, action, entity type and a created_at range, plus pagination.
func (s *Server) handleListLogs(c *fiber.Ctx) error {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	var actor pgtype.UUID
	if v := c.Query("actor"); v != "" {
		id, err := uuidFromString(v)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid actor"})
		}
		actor = id
	}

	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid from"})
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid to"})
	}

	rows, err := s.Queries.ListAuditLogs(c.Context(), db.ListAuditLogsParams{
		Column1: actor,
		Column2: c.Query("action"),
		Column3: c.Query("entity"),
		Column4: from,
		Column5: to,
		Limit:   int32(limit),
		Offset:  int32(offset),
	})
	if err != nil {
		log.Printf("logs: list: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	items := make([]auditItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, toAuditItem(r))
	}
	return c.JSON(fiber.Map{"items": items, "limit": limit, "offset": offset})
}
