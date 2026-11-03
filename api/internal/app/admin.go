package app

import (
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/feature"
)

// Dashboard (T14.5): key metrics for the admin panel.
type dashboardData struct {
	UsersCount        int64 `json:"users_count"`
	BlockedUsersCount int64 `json:"blocked_users_count"`
	PendingRequests   int64 `json:"pending_requests_count"`
	LoginsToday       int64 `json:"logins_today"`
	LoginsTotal       int64 `json:"logins_total"`
}

// handleDashboard returns aggregated metrics (admin only).
func (s *Server) handleDashboard(c *fiber.Ctx) error {
	ctx := c.Context()
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	usersCount, err := s.Queries.CountUsers(ctx)
	if err != nil {
		log.Printf("dashboard: users: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	blockedCount, err := s.Queries.CountBlockedUsers(ctx)
	if err != nil {
		log.Printf("dashboard: blocked: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	pending, err := s.Queries.CountPendingRegistrationRequests(ctx)
	if err != nil {
		log.Printf("dashboard: pending: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	loginsToday, err := s.Queries.CountSuccessfulLoginsSince(ctx, pgtype.Timestamptz{Time: startOfDay, Valid: true})
	if err != nil {
		log.Printf("dashboard: logins today: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	loginsTotal, err := s.Queries.CountLoginsTotal(ctx)
	if err != nil {
		log.Printf("dashboard: logins total: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	return c.JSON(dashboardData{
		UsersCount:        usersCount,
		BlockedUsersCount: blockedCount,
		PendingRequests:   pending,
		LoginsToday:       loginsToday,
		LoginsTotal:       loginsTotal,
	})
}

// handleListRegistrationRequests lists pending and reviewed requests (admin
// only, T14.2).
func (s *Server) handleListRegistrationRequests(c *fiber.Ctx) error {
	requests, err := s.Queries.ListRegistrationRequests(c.Context())
	if err != nil {
		log.Printf("registration-requests: list: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	items := make([]fiber.Map, 0, len(requests))
	for _, r := range requests {
		items = append(items, fiber.Map{
			"id":          r.ID.String(),
			"first_name":  r.FirstName,
			"last_name":   r.LastName,
			"middle_name": r.MiddleName.String,
			"email":       r.Email,
			"telegram":    r.Telegram.String,
			"department":  r.Department.String,
			"position":    r.Position.String,
			"reason":      r.Reason.String,
			"status":      r.Status,
			"created_at":  r.CreatedAt.Time,
		})
	}
	return c.JSON(fiber.Map{"requests": items})
}

type registrationReviewRequest struct {
	Status string `json:"status"`
}

// handleUpdateRegistrationRequest approves or rejects a registration request
// (admin only). Approval provisions a user; a rejected request stays for the
// record.
func (s *Server) handleUpdateRegistrationRequest(c *fiber.Ctx) error {
	id, err := uuidFromString(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request id"})
	}

	var req registrationReviewRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.Status != "approved" && req.Status != "rejected" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be approved or rejected"})
	}

	request, err := s.Queries.GetRegistrationRequest(c.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "request not found"})
	}
	if err != nil {
		log.Printf("registration-requests: get: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if request.Status != "pending" {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "request is already reviewed"})
	}

	actor := adminID(c)
	reviewed, err := s.Queries.UpdateRegistrationRequestStatus(c.Context(), db.UpdateRegistrationRequestStatusParams{
		ID:         id,
		Status:     req.Status,
		ReviewedBy: actor,
	})
	if err != nil {
		log.Printf("registration-requests: update: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	action := auditRegistrationRejected
	if req.Status == "approved" {
		action = auditRegistrationApproved
		if _, err := s.Queries.CreateUser(c.Context(), db.CreateUserParams{
			FirstName:  request.FirstName,
			LastName:   request.LastName,
			MiddleName: request.MiddleName,
			Email:      request.Email,
			Telegram:   request.Telegram,
			Department: request.Department,
			Position:   request.Position,
			Role:       "user",
		}); err != nil {
			log.Printf("registration-requests: create user: %v", err)
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "email already exists or invalid data"})
		}
	}

	s.recordAudit(c, actor, action, "registration", request.ID.String(), map[string]any{
		"email": request.Email,
	})

	return c.JSON(fiber.Map{
		"request": fiber.Map{
			"id":     reviewed.ID.String(),
			"email":  reviewed.Email,
			"status": reviewed.Status,
		},
	})
}

// Settings (T14.1): only known keys can be read or written by an admin.
var settingsWhitelist = map[string]bool{
	consentDocumentVersionKey: true,
	feature.TelegramBot:       true,
}

// handleGetSetting returns a setting value (admin only).
func (s *Server) handleGetSetting(c *fiber.Ctx) error {
	key := c.Params("key")
	if !settingsWhitelist[key] {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "setting not found"})
	}

	setting, err := s.Queries.GetSetting(c.Context(), key)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "setting not found"})
	}
	if err != nil {
		log.Printf("settings: get: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	var value string
	if err := json.Unmarshal(setting.Value, &value); err != nil {
		value = string(setting.Value)
	}
	return c.JSON(fiber.Map{"key": key, "value": value})
}

type settingUpdateRequest struct {
	Value string `json:"value"`
}

// handlePutSetting updates a setting (admin only).
func (s *Server) handlePutSetting(c *fiber.Ctx) error {
	key := c.Params("key")
	if !settingsWhitelist[key] {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "setting not found"})
	}

	var req settingUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.Value == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "value is required"})
	}

	value, err := json.Marshal(req.Value)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	if err := s.Queries.SetSetting(c.Context(), db.SetSettingParams{
		Key:   key,
		Value: value,
	}); err != nil {
		log.Printf("settings: set: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	s.recordAudit(c, adminID(c), auditSettings, "setting", key, map[string]any{"value": req.Value})

	return c.JSON(fiber.Map{"key": key, "value": req.Value})
}
