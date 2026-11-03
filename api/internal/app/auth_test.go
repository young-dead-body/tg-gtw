package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/session"
)

type testHarness struct {
	app interface {
		Test(req *http.Request, config ...int) (*http.Response, error)
	}
	sessions *fakeSessionStore
	queries  *fakeQuerier
	notifier *fakeNotifier
}

func buildHarness(t *testing.T) *testHarness {
	t.Helper()
	store := newFakeSessionStore()
	queries := &fakeQuerier{}
	notifier := newFakeNotifier()
	application := New(&Server{
		Queries:  queries,
		Sessions: store,
		Hasher:   password.New(),
		Notifier: notifier,
		Config:   Config{SessionTTL: time.Hour, PasswordReuseHistory: 5},
	}, HealthCheckers{})
	return &testHarness{app: application, sessions: store, queries: queries, notifier: notifier}
}

func (h *testHarness) call(method, path string, body string, cookies ...*http.Cookie) (*http.Response, error) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	cookies = addTestCSRF(method, req, cookies)
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	return h.app.Test(req, 1000)
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func testCookie(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func TestLoginSuccessSetsSessionCookie(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, _ := hasher.Hash("secret-pass")
	provisioned := fakeUser
	provisioned.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return provisioned, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, readAll(t, resp))
	}

	cookie := testCookie(t, resp, CookieName)
	if !cookie.HttpOnly {
		t.Error("expected HttpOnly session cookie")
	}
	if !cookie.Secure {
		t.Error("expected Secure session cookie")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite=Strict, got %v", cookie.SameSite)
	}

	// The session must be stored server-side.
	if _, err := h.sessions.Get(context.Background(), cookie.Value); err != nil {
		t.Errorf("expected session in store, got error: %v", err)
	}

	body := decodeBody(t, resp)
	user := body["user"].(map[string]any)
	if user["email"] != "ivan@example.com" {
		t.Errorf("unexpected user email %v", user["email"])
	}
	if body["consent_required"] != false {
		t.Errorf("expected consent_required=false for a user with consent, got %v", body["consent_required"])
	}
}

func TestLoginConsentRequiredFlag(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, _ := hasher.Hash("secret-pass")
	provisioned := fakeUser
	provisioned.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return provisioned, nil
	}
	// The account has no consent record (anonymous linking did not match).
	falseVal := false
	h.queries.hasConsent = &falseVal

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["consent_required"] != true {
		t.Errorf("expected consent_required=true without a consent record, got %v", body["consent_required"])
	}
}

func TestLoginUnknownEmail(t *testing.T) {
	h := buildHarness(t)
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return db.User{}, pgx.ErrNoRows
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"nobody@example.com","password":"x"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLoginDisabledAccount(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, _ := hasher.Hash("secret-pass")
	blocked := fakeUser
	blocked.IsActive = false
	blocked.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return blocked, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestLoginMissingEmail(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/login", `{"password":"x"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMeRequiresAuth(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodGet, "/me", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMeWithValidSession(t *testing.T) {
	h := buildHarness(t)
	ctx := context.Background()
	sessionID, err := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resp, err := h.call(http.MethodGet, "/me", "", &http.Cookie{Name: CookieName, Value: sessionID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["user"].(map[string]any)["email"] != "ivan@example.com" {
		t.Errorf("unexpected /me response: %v", body)
	}
	if body["consent_required"] != false {
		t.Errorf("expected consent_required=false for a user with consent, got %v", body["consent_required"])
	}
}

func TestMeConsentRequiredFlag(t *testing.T) {
	h := buildHarness(t)
	ctx := context.Background()
	sessionID, err := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	falseVal := false
	h.queries.hasConsent = &falseVal

	resp, err := h.call(http.MethodGet, "/me", "", &http.Cookie{Name: CookieName, Value: sessionID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["consent_required"] != true {
		t.Errorf("expected consent_required=true without a consent record, got %v", body["consent_required"])
	}
}

func TestMeWithUnknownSession(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodGet, "/me", "", &http.Cookie{Name: CookieName, Value: "sess_missing"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	h := buildHarness(t)
	ctx := context.Background()
	sessionID, err := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resp, err := h.call(http.MethodPost, "/logout", "", &http.Cookie{Name: CookieName, Value: sessionID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if _, err := h.sessions.Get(ctx, sessionID); err != session.ErrNotFound {
		t.Errorf("expected session removed, got err=%v", err)
	}

	cookie := testCookie(t, resp, CookieName)
	if cookie.Value != "" {
		t.Errorf("expected cleared cookie value, got %q", cookie.Value)
	}
}

func TestLogoutRequiresAuth(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/logout", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, err := hasher.Hash("correct")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return user, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"wrong"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLoginAcceptsCorrectPassword(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, err := hasher.Hash("correct-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return user, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"correct-password"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLoginRejectsUnprovisionedAccount(t *testing.T) {
	h := buildHarness(t)
	user := fakeUser // no password hash set
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return user, nil
	}

	resp, _ := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"anything"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for account without password, got %d", resp.StatusCode)
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	currentHash, _ := hasher.Hash("current-pass")
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: currentHash, Valid: true}
	h.queries.getByIDFn = func(ctx context.Context, id pgtype.UUID) (db.User, error) {
		return user, nil
	}
	ctx := context.Background()
	sid, err := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resp, err := h.call(http.MethodPost, "/me/password",
		`{"current_password":"current-pass","new_password":"brand-new-pass"} `, &http.Cookie{Name: CookieName, Value: sid})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	currentHash, _ := hasher.Hash("actual-pass")
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: currentHash, Valid: true}
	h.queries.getByIDFn = func(ctx context.Context, id pgtype.UUID) (db.User, error) {
		return user, nil
	}
	ctx := context.Background()
	sid, _ := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)

	resp, err := h.call(http.MethodPost, "/me/password",
		`{"current_password":"wrong","new_password":"new-pass-1234"}`, &http.Cookie{Name: CookieName, Value: sid})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestChangePasswordReuseRejected(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	currentHash, _ := hasher.Hash("current-pass")
	reusedHash, _ := hasher.Hash("old-reused-pass")
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: currentHash, Valid: true}
	h.queries.getByIDFn = func(ctx context.Context, id pgtype.UUID) (db.User, error) {
		return user, nil
	}
	h.queries.listRecentFn = func(ctx context.Context, arg db.ListRecentPasswordHashesParams) ([]string, error) {
		return []string{reusedHash}, nil
	}
	ctx := context.Background()
	sid, _ := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)

	resp, err := h.call(http.MethodPost, "/me/password",
		`{"current_password":"current-pass","new_password":"old-reused-pass"}`, &http.Cookie{Name: CookieName, Value: sid})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestChangePasswordReuseCurrentRejected(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	currentHash, _ := hasher.Hash("current-pass")
	user := fakeUser
	user.PasswordHash = pgtype.Text{String: currentHash, Valid: true}
	h.queries.getByIDFn = func(ctx context.Context, id pgtype.UUID) (db.User, error) {
		return user, nil
	}
	ctx := context.Background()
	sid, _ := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)

	resp, err := h.call(http.MethodPost, "/me/password",
		`{"current_password":"current-pass","new_password":"current-pass"}`, &http.Cookie{Name: CookieName, Value: sid})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	h := buildHarness(t)
	ctx := context.Background()
	sid, _ := h.sessions.Create(ctx, fakeUser.ID.String(), time.Hour)

	resp, err := h.call(http.MethodPost, "/me/password",
		`{"current_password":"x","new_password":"short"}`, &http.Cookie{Name: CookieName, Value: sid})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMeWithBlockedUserAfterLogin(t *testing.T) {
	h := buildHarness(t)
	blocked := fakeUser
	blockedAt := time.Now()
	blocked.BlockedAt = pgtype.Timestamptz{Time: blockedAt, Valid: true}
	h.queries.getByIDFn = func(ctx context.Context, id pgtype.UUID) (db.User, error) {
		return blocked, nil
	}
	ctx := context.Background()
	sessionID, err := h.sessions.Create(ctx, blocked.ID.String(), time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resp, err := h.call(http.MethodGet, "/me", "", &http.Cookie{Name: CookieName, Value: sessionID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a blocked account, got %d", resp.StatusCode)
	}
}

func TestLoginRejectsExpiredPassword(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, err := hasher.Hash("secret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	expired := fakeUser
	expired.PasswordHash = pgtype.Text{String: hash, Valid: true}
	expired.PasswordChangedAt = pgtype.Timestamptz{Time: time.Now().Add(-200 * 24 * time.Hour), Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return expired, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["error"] != "password_expired" {
		t.Errorf("expected password_expired, got %v", body["error"])
	}
}

func TestLoginRejectsExpiredAccount(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, err := hasher.Hash("secret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	expired := fakeUser
	expired.PasswordHash = pgtype.Text{String: hash, Valid: true}
	expired.ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return expired, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestLoginAcceptsFreshPassword(t *testing.T) {
	h := buildHarness(t)
	hasher := password.New()
	hash, err := hasher.Hash("secret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	fresh := fakeUser
	fresh.PasswordHash = pgtype.Text{String: hash, Valid: true}
	fresh.PasswordChangedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return fresh, nil
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"secret-pass"}`, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
