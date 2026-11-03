package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/policy"
	"github.com/company/telegram-gateway/api/internal/session"
)

const userLocalsKey = "currentUser"

type userResponse struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	MiddleName string `json:"middle_name"`
	Telegram   string `json:"telegram"`
	Department string `json:"department"`
	Position   string `json:"position"`
	Role       string `json:"role"`
	IsActive   bool   `json:"is_active"`
	BlockedAt  string `json:"blocked_at"`
	ExpiresAt  string `json:"expires_at"`
}

func toUserResponse(u db.User) userResponse {
	return userResponse{
		ID:         u.ID.String(),
		Email:      u.Email,
		FirstName:  u.FirstName,
		LastName:   u.LastName,
		MiddleName: u.MiddleName.String,
		Telegram:   u.Telegram.String,
		Department: u.Department.String,
		Position:   u.Position.String,
		Role:       u.Role,
		IsActive:   u.IsActive,
		BlockedAt:  timeString(u.BlockedAt),
		ExpiresAt:  timeString(u.ExpiresAt),
	}
}

// timeString formats a timestamptz as RFC3339, empty when invalid.
func timeString(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format(time.RFC3339)
}

func (s *Server) setSessionCookie(c *fiber.Ctx, sessionID string, ttl time.Duration) {
	c.Cookie(&fiber.Cookie{
		Name:     CookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  time.Now().Add(ttl),
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
	})
}

func (s *Server) clearSessionCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin creates a server-side session and sets the HttpOnly cookie.
func (s *Server) handleLogin(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "email is required"})
	}

	// Brute-force lockout (T15.2): IP and account are blocked for a window
	// after too many consecutive failures. The email check happens before the
	// DB lookup so locked accounts are not probed at all.
	if s.Lockout != nil {
		blocked, err := s.lockoutBlocked(c, "ip:"+c.IP())
		if err != nil {
			return err
		}
		if blocked {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "too many failed attempts, try later"})
		}
		blocked, err = s.lockoutBlocked(c, "email:"+req.Email)
		if err != nil {
			return err
		}
		if blocked {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "too many failed attempts, try later"})
		}
	}

	u, err := s.Queries.GetUserByEmail(c.Context(), req.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		s.recordFailedLogin(c, db.User{Email: req.Email}, "user_not_found")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid credentials"})
	}
	if err != nil {
		log.Printf("login: get user: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	if ok, err := s.verifyPassword(c, u, req.Password); err != nil || !ok {
		if errors.Is(err, password.ErrBusy) {
			return passwordBusy(c)
		}
		s.recordFailedLogin(c, u, "invalid_credentials")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid credentials"})
	}

	now := time.Now()
	if !u.IsActive {
		s.recordFailedLogin(c, u, "account_disabled")
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "account is disabled"})
	}
	if u.BlockedAt.Valid {
		s.recordFailedLogin(c, u, "account_blocked")
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "account is blocked"})
	}
	if u.ExpiresAt.Valid && u.ExpiresAt.Time.Before(now) {
		s.recordFailedLogin(c, u, "access_expired")
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "access expired"})
	}

	// Password rotation (ТЗ §8): a user with an expired password cannot log in
	// until it is rotated by an administrator.
	pol, err := s.passwordPolicy(c.Context())
	if err != nil {
		log.Printf("login: load password policy: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	changedAt := u.PasswordChangedAt
	if !changedAt.Valid {
		changedAt = u.CreatedAt
	}
	if changedAt.Valid && !changedAt.Time.Add(pol.RotationPeriod).After(now) {
		s.recordFailedLogin(c, u, "password_expired")
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error":   "password_expired",
			"message": "пароль истёк, обратитесь к администратору",
		})
	}

	ttl := s.Config.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var sessionID string
	if versioned, ok := s.Sessions.(session.VersionedStore); ok {
		sessionID, err = versioned.CreateVersioned(c.Context(), u.ID.String(), ttl, session.CredentialRevision(u.PasswordHash.String, u.PasswordChangedAt.Time))
	} else {
		sessionID, err = s.Sessions.Create(c.Context(), u.ID.String(), ttl)
	}
	if err != nil {
		log.Printf("login: create session: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	// A successful login clears the brute-force counters (T15.2).
	if s.Lockout != nil {
		if err := s.Lockout.Reset(c.Context(), "ip:"+c.IP()); err != nil {
			log.Printf("lockout: reset ip: %v", err)
		}
		if err := s.Lockout.Reset(c.Context(), "email:"+u.Email); err != nil {
			log.Printf("lockout: reset email: %v", err)
		}
	}

	s.recordLoginStatistics(c, u.ID, "")

	// Attribute any anonymous consent accepted from this device to the user so
	// the proxy gate (S10) can prove "session + accepted consent" (ТЗ §11).
	s.linkConsent(c, u.ID)

	// The linking is best-effort (device heuristic), so the response tells the
	// SPA whether the identified user still lacks a consent record: if so, the
	// consent banner is shown again and POST /consent records it against the
	// session before /tg/ is reached.
	consentRequired := s.userConsentRequired(c.Context(), u)

	s.setSessionCookie(c, sessionID, ttl)
	return c.JSON(fiber.Map{"user": toUserResponse(u), "consent_required": consentRequired})
}

func (s *Server) handleLogout(c *fiber.Ctx) error {
	u := c.Locals(userLocalsKey).(db.User)

	if err := s.Sessions.Delete(c.Context(), c.Cookies(CookieName)); err != nil {
		log.Printf("logout: delete session: %v", err)
	}

	// Close the last open session in statistics (ТЗ §12: длительность сессии).
	if err := s.Queries.UpdateStatisticsDuration(c.Context(), u.ID); err != nil {
		log.Printf("logout: update stats duration: %v", err)
	}

	s.recordAudit(c, u.ID, auditLogout, "user", u.ID.String(), nil)

	s.clearSessionCookie(c)
	return c.JSON(fiber.Map{"user": toUserResponse(u)})
}

func (s *Server) handleMe(c *fiber.Ctx) error {
	u := c.Locals(userLocalsKey).(db.User)
	return c.JSON(fiber.Map{"user": toUserResponse(u), "consent_required": s.userConsentRequired(c.Context(), u)})
}

// errUnauthorized is returned by authenticate when there is no valid session.
var errUnauthorized = errors.New("unauthorized")

// authenticate validates the session cookie and returns the associated user.
// It does not call c.Next(), so it is safe to reuse from nested middlewares.
func (s *Server) authenticate(c *fiber.Ctx) (db.User, error) {
	sessionID := c.Cookies(CookieName)
	if sessionID == "" {
		return db.User{}, errUnauthorized
	}

	userID, err := s.Sessions.Get(c.Context(), sessionID)
	if errors.Is(err, session.ErrNotFound) {
		return db.User{}, errUnauthorized
	}
	if err != nil {
		return db.User{}, fmt.Errorf("get session: %w", err)
	}

	id, err := uuidFromString(userID)
	if err != nil {
		return db.User{}, errUnauthorized
	}

	u, err := s.Queries.GetUserByID(c.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, errUnauthorized
	}
	if err != nil {
		return db.User{}, fmt.Errorf("get user: %w", err)
	}

	if versioned, ok := s.Sessions.(session.VersionedStore); ok {
		revision, err := versioned.Revision(c.Context(), sessionID)
		if err != nil || revision != session.CredentialRevision(u.PasswordHash.String, u.PasswordChangedAt.Time) {
			return db.User{}, errUnauthorized
		}
	}
	return u, nil
}

// requireAuth validates the session cookie, loads the user and stores it in
// the request locals. Unauthenticated requests get a 401.
func (s *Server) requireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		u, err := s.authenticate(c)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
			}
			log.Printf("auth: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
		}
		if !accountEnabled(u) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "account disabled or expired"})
		}
		c.Locals(userLocalsKey, u)
		return c.Next()
	}
}

func uuidFromString(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

// verifyPassword checks a plaintext password against the user's stored hash.
// Users without a stored hash (e.g. not yet provisioned) cannot authenticate.
func (s *Server) verifyPassword(c *fiber.Ctx, u db.User, plain string) (bool, error) {
	if !u.PasswordHash.Valid || u.PasswordHash.String == "" {
		return false, nil
	}
	return s.Hasher.Verify(plain, u.PasswordHash.String)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// Sentinel errors returned by rotatePassword.
var (
	errWrongCurrentPassword = errors.New("current password is incorrect")
	errPasswordReuse        = errors.New("password was used recently")
)

// handleChangePassword lets an authenticated user rotate their password,
// enforcing that the new password has not been used recently.
func (s *Server) handleChangePassword(c *fiber.Ctx) error {
	var req changePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.NewPassword == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "new_password is required"})
	}
	if len(req.NewPassword) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "new_password must be at least 8 characters"})
	}

	u, err := s.currentUser(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}

	if err := s.rotatePassword(c, u, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, password.ErrBusy):
			return passwordBusy(c)
		case errors.Is(err, errWrongCurrentPassword):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": errWrongCurrentPassword.Error()})
		case errors.Is(err, errPasswordReuse):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": errPasswordReuse.Error()})
		default:
			log.Printf("rotate: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
		}
	}

	s.recordAudit(c, u.ID, auditPassword, "user", u.ID.String(), nil)

	return c.JSON(fiber.Map{"message": "password changed"})
}

// rotatePassword performs the Argon2id change and enforces the reuse policy.
func (s *Server) rotatePassword(c *fiber.Ctx, u db.User, current, newPass string) error {
	if ok, err := s.verifyPassword(c, u, current); err != nil {
		return fmt.Errorf("verify current password: %w", err)
	} else if !ok {
		return errWrongCurrentPassword
	}

	reuse, err := s.isPasswordReused(c.Context(), u, newPass)
	if err != nil {
		return fmt.Errorf("check history: %w", err)
	}
	if reuse {
		return errPasswordReuse
	}

	hash, err := s.Hasher.Hash(newPass)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}

	if err := s.Sessions.DeleteUser(c.Context(), u.ID.String()); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if _, err := s.Queries.UpdateUserPassword(c.Context(), db.UpdateUserPasswordParams{
		ID:           u.ID,
		PasswordHash: pgtype.Text{String: hash, Valid: true},
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	s.clearSessionCookie(c)

	return nil
}

// isPasswordReused reports whether the candidate matches the current password
// or any password the user used within the configured history window.
func (s *Server) isPasswordReused(ctx context.Context, u db.User, candidate string) (bool, error) {
	if u.PasswordHash.Valid && u.PasswordHash.String != "" {
		ok, err := s.Hasher.Verify(candidate, u.PasswordHash.String)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}

	history, err := s.Queries.ListRecentPasswordHashes(ctx, db.ListRecentPasswordHashesParams{
		UserID: u.ID,
		Limit:  int32(s.reuseHistoryLimit()),
	})
	if err != nil {
		return false, err
	}

	for _, h := range history {
		ok, err := s.Hasher.Verify(candidate, h)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) currentUser(c *fiber.Ctx) (db.User, error) {
	u, ok := c.Locals(userLocalsKey).(db.User)
	if !ok {
		return db.User{}, errors.New("no user in context")
	}
	return u, nil
}

func (s *Server) reuseHistoryLimit() int {
	if s.Config.PasswordReuseHistory > 0 {
		return s.Config.PasswordReuseHistory
	}
	return 5
}

// passwordPolicy loads the password rotation policy from settings, falling
// back to sensible defaults when no value is configured.
func (s *Server) passwordPolicy(ctx context.Context) (policy.Password, error) {
	return policy.LoadPassword(ctx, s.Queries)
}

// recordPasswordChange updates the change timestamp used for rotation. It is
// called whenever a password is set or rotated outside the self-service path.
func (s *Server) recordPasswordChange(ctx context.Context, userID pgtype.UUID) error {
	return s.Queries.TouchPasswordChangedAt(ctx, userID)
}

// lockoutBlocked reports whether the key is locked out, translating store
// errors into a 500 response.
func (s *Server) lockoutBlocked(c *fiber.Ctx, key string) (bool, error) {
	blocked, err := s.Lockout.Blocked(c.Context(), key)
	if err != nil {
		log.Printf("lockout: check %s: %v", key, err)
		return false, c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal error"})
	}
	return blocked, nil
}

// recordFailedLogin records the statistics row and, when lockout is enabled,
// counts the failure against the caller IP and the account.
func (s *Server) recordFailedLogin(c *fiber.Ctx, u db.User, authError string) {
	s.recordLoginStatistics(c, u.ID, authError)
	if s.Lockout == nil {
		return
	}
	if err := s.Lockout.RecordFailure(c.Context(), "ip:"+c.IP()); err != nil {
		log.Printf("lockout: record ip: %v", err)
	}
	if u.Email != "" {
		if err := s.Lockout.RecordFailure(c.Context(), "email:"+u.Email); err != nil {
			log.Printf("lockout: record email: %v", err)
		}
	}
}

// userConsentRequired reports whether the identified user still lacks an
// accepted consent record (ТЗ §11). Anonymous consent accepted before login is
// linked to the account by a device heuristic (linkConsent), which is best
// effort: when it fails (different device/browser, cleared anonymous rows,
// shared NAT), the SPA must be able to request the consent again from an
// authenticated user so POST /consent can record it against their account.
func (s *Server) userConsentRequired(ctx context.Context, u db.User) bool {
	ok, err := s.Queries.HasUserConsent(ctx, u.ID)
	if err != nil {
		log.Printf("consent: check consent for %s: %v", u.ID.String(), err)
		return true
	}
	return !ok
}

// linkConsent consumes an unguessable receipt issued to this browser, never
// identifying a person by shared network/browser attributes.
func (s *Server) linkConsent(c *fiber.Ctx, userID pgtype.UUID) {
	id, err := uuidFromString(c.Cookies(consentReceiptCookie))
	if err != nil {
		return
	}
	if err := s.Queries.LinkConsentToUser(c.Context(), db.LinkConsentToUserParams{UserID: userID, ConsentID: id}); err != nil {
		log.Printf("login: link consent: %v", err)
		return
	}
	c.Cookie(&fiber.Cookie{Name: consentReceiptCookie, Path: "/", Expires: time.Unix(0, 0), Secure: true, HTTPOnly: true, SameSite: "Strict"})
}
