package app

import (
	"log"
	"net/mail"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// Telegram handle validation: allows optional leading @, 5–32 chars,
// letters, digits and underscore.
var telegramRegexp = regexp.MustCompile(`^@?[a-zA-Z0-9_]{5,32}$`)

type registrationRequest struct {
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	MiddleName string `json:"middle_name"`
	Email      string `json:"email"`
	Telegram   string `json:"telegram"`
	Department string `json:"department"`
	Position   string `json:"position"`
	Reason     string `json:"reason"`
}

type registrationFieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// handleRegistration accepts a user registration request (ТЗ §6) and stores it
// for administrative review with status "pending".
func (s *Server) handleRegistration(c *fiber.Ctx) error {
	var req registrationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}

	if validations := validateRegistration(req); len(validations) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"errors": validations})
	}

	created, err := s.Queries.CreateRegistrationRequest(c.Context(), db.CreateRegistrationRequestParams{
		FirstName:  strings.TrimSpace(req.FirstName),
		LastName:   strings.TrimSpace(req.LastName),
		MiddleName: pgtext(req.MiddleName),
		Email:      strings.TrimSpace(req.Email),
		Telegram:   pgtext(req.Telegram),
		Department: pgtext(req.Department),
		Position:   pgtext(req.Position),
		Reason:     pgtext(req.Reason),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	// Notify the administrator. Delivery (email/Telegram bot) is wired in S11;
	// the notifier stub guarantees the event is at least logged. A notification
	// failure must not break the registration flow.
	if err := s.Notifier.Notify(c.Context(), "registration.created", created.ID.String()); err != nil {
		log.Printf("registration: notify admin: %v", err)
	}

	s.recordAudit(c, pgtype.UUID{}, auditRegistration, "registration", created.ID.String(), map[string]any{
		"email": created.Email,
	})

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"request": fiber.Map{
			"id":         created.ID.String(),
			"email":      created.Email,
			"status":     created.Status,
			"created_at": created.CreatedAt.Time,
		},
	})
}

func validateRegistration(req registrationRequest) []registrationFieldError {
	var errs []registrationFieldError
	add := func(field, msg string) {
		errs = append(errs, registrationFieldError{Field: field, Message: msg})
	}

	if strings.TrimSpace(req.FirstName) == "" {
		add("first_name", "обязательное поле")
	}
	if strings.TrimSpace(req.LastName) == "" {
		add("last_name", "обязательное поле")
	}
	if strings.TrimSpace(req.Email) == "" {
		add("email", "обязательное поле")
	} else if _, err := mail.ParseAddress(req.Email); err != nil {
		add("email", "некорректный email")
	}
	if strings.TrimSpace(req.Telegram) != "" && !telegramRegexp.MatchString(strings.TrimSpace(req.Telegram)) {
		add("telegram", "некорректный telegram username")
	}
	if strings.TrimSpace(req.Reason) == "" {
		add("reason", "обязательное поле")
	}

	return errs
}

// pgtext wraps a string into pgtype.Text, treating empty as NULL.
func pgtext(s string) pgtype.Text {
	trimmed := strings.TrimSpace(s)
	return pgtype.Text{String: trimmed, Valid: trimmed != ""}
}
