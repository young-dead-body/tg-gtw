package app

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// requireAdmin extends requireAuth with the admin role check.
func (s *Server) requireAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		u, err := s.authenticate(c)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
			}
			log.Printf("auth: %v", err)
			return passwordError(c, err)
		}
		if u.Role != "admin" || !accountEnabled(u) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
		}
		c.Locals(userLocalsKey, u)
		return c.Next()
	}
}

type createUserRequest struct {
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	MiddleName string `json:"middle_name"`
	Email      string `json:"email"`
	Telegram   string `json:"telegram"`
	Department string `json:"department"`
	Position   string `json:"position"`
	Role       string `json:"role"`
	Password   string `json:"password"`
	ExpiresAt  string `json:"expires_at"`
}

type updateUserRequest struct {
	MiddleName *string `json:"middle_name"`
	Telegram   *string `json:"telegram"`
	Department *string `json:"department"`
	Position   *string `json:"position"`
	Role       *string `json:"role"`
	Blocked    *bool   `json:"blocked"`
	IsActive   *bool   `json:"is_active"`
	ExpiresAt  *string `json:"expires_at"`
	Password   *string `json:"password"`
}

// handleListUsers returns all users (admin only).
func (s *Server) handleListUsers(c *fiber.Ctx) error {
	users, err := s.Queries.ListUsers(c.Context())
	if err != nil {
		log.Printf("list users: %v", err)
		return passwordError(c, err)
	}
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
	}
	return c.JSON(fiber.Map{"users": out})
}

// handleCreateUser provisions a user (admin only).
func (s *Server) handleCreateUser(c *fiber.Ctx) error {
	var req createUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.TrimSpace(req.Email)

	if req.FirstName == "" || req.LastName == "" || req.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "first_name, last_name and email are required"})
	}
	if req.Role != "" && req.Role != "user" && req.Role != "admin" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be user or admin"})
	}
	role := req.Role
	if role == "" {
		role = "user"
	}
	if req.Password != "" && len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "password must be at least 8 characters"})
	}

	expiresAt, err := parseOptionalTime(req.ExpiresAt)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid expires_at"})
	}

	var hash pgtype.Text
	if req.Password != "" {
		h, err := s.Hasher.Hash(req.Password)
		if err != nil {
			log.Printf("create user: hash password: %v", err)
			return passwordError(c, err)
		}
		hash = pgtype.Text{String: h, Valid: true}
	}

	user, err := s.Queries.CreateUser(c.Context(), db.CreateUserParams{
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		MiddleName:   pgtext(req.MiddleName),
		Email:        req.Email,
		Telegram:     pgtext(req.Telegram),
		Department:   pgtext(req.Department),
		Position:     pgtext(req.Position),
		Role:         role,
		PasswordHash: hash,
		ExpiresAt:    expiresAt,
	})
	if err != nil {
		log.Printf("create user: %v", err)
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "email already exists or invalid data"})
	}

	if req.Password != "" {
		if _, err := s.Queries.InsertPasswordHistory(c.Context(), db.InsertPasswordHistoryParams{
			UserID:       user.ID,
			PasswordHash: hash.String,
		}); err != nil {
			log.Printf("create user: history: %v", err)
		}
		if err := s.recordPasswordChange(c.Context(), user.ID); err != nil {
			log.Printf("create user: record password change: %v", err)
		}
	}

	s.recordAudit(c, adminID(c), auditUserCreated, "user", user.ID.String(), map[string]any{
		"email": user.Email,
	})

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"user": toUserResponse(user)})
}

// handleUpdateUser applies admin edits to a user (admin only).
func (s *Server) handleUpdateUser(c *fiber.Ctx) error {
	id, err := uuidFromString(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user id"})
	}

	var req updateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}

	u, err := s.Queries.GetUserByID(c.Context(), id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
	}

	// Resolve all editable fields to concrete values.
	middleName := u.MiddleName
	if req.MiddleName != nil {
		middleName = pgtext(*req.MiddleName)
	}
	telegram := u.Telegram
	if req.Telegram != nil {
		telegram = pgtext(*req.Telegram)
	}
	department := u.Department
	if req.Department != nil {
		department = pgtext(*req.Department)
	}
	position := u.Position
	if req.Position != nil {
		position = pgtext(*req.Position)
	}
	role := u.Role
	if req.Role != nil {
		if *req.Role != "user" && *req.Role != "admin" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "role must be user or admin"})
		}
		role = *req.Role
	}

	isActive := u.IsActive
	blockedAt := u.BlockedAt
	if req.Blocked != nil {
		if *req.Blocked {
			now := time.Now()
			blockedAt = pgtype.Timestamptz{Time: now, Valid: true}
			isActive = false
		} else {
			blockedAt = pgtype.Timestamptz{}
			isActive = true
		}
	}
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	expiresAt := u.ExpiresAt
	if req.ExpiresAt != nil {
		parsed, err := parseOptionalTime(*req.ExpiresAt)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid expires_at"})
		}
		expiresAt = parsed
	}

	hash := u.PasswordHash
	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < 8 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "password must be at least 8 characters"})
		}
		reused, err := s.isPasswordReused(c.Context(), u, *req.Password)
		if err != nil {
			log.Printf("update user: history: %v", err)
			return passwordError(c, err)
		}
		if reused {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "password was used recently and cannot be reused"})
		}
		h, err := s.Hasher.Hash(*req.Password)
		if err != nil {
			log.Printf("update user: hash password: %v", err)
			return passwordError(c, err)
		}
		hash = pgtype.Text{String: h, Valid: true}
	}

	// Password resets and account restrictions revoke every existing session.
	if (req.Password != nil && *req.Password != "") || !isActive || blockedAt.Valid {
		if err := s.Sessions.DeleteUser(c.Context(), u.ID.String()); err != nil {
			log.Printf("update user: revoke sessions: %v", err)
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "session revocation unavailable"})
		}
	}

	updated, err := s.Queries.UpdateUserProfile(c.Context(), db.UpdateUserProfileParams{
		ID:           u.ID,
		FirstName:    u.FirstName,
		LastName:     u.LastName,
		MiddleName:   middleName,
		Telegram:     telegram,
		Department:   department,
		Position:     position,
		Role:         role,
		IsActive:     isActive,
		BlockedAt:    blockedAt,
		ExpiresAt:    expiresAt,
		PasswordHash: hash,
	})
	if err != nil {
		log.Printf("update user: %v", err)
		return passwordError(c, err)
	}

	s.recordAudit(c, adminID(c), auditUserUpdated, "user", u.ID.String(), map[string]any{
		"role":      role,
		"is_active": isActive,
	})

	return c.JSON(fiber.Map{"user": toUserResponse(updated)})
}

// handleDeleteUser removes a user and revokes sessions (admin only).
func (s *Server) handleDeleteUser(c *fiber.Ctx) error {
	id, err := uuidFromString(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid user id"})
	}

	deleted, err := s.Queries.DeleteUser(c.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user not found"})
		}
		log.Printf("delete user: %v", err)
		return passwordError(c, err)
	}

	if err := s.Sessions.DeleteUser(c.Context(), deleted.ID.String()); err != nil {
		log.Printf("delete user: revoke sessions: %v", err)
	}

	s.recordAudit(c, adminID(c), auditUserDeleted, "user", deleted.ID.String(), nil)

	return c.JSON(fiber.Map{"user": toUserResponse(deleted)})
}

func parseOptionalTime(raw string) (pgtype.Timestamptz, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return pgtype.Timestamptz{}, nil
	}
	t, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}
