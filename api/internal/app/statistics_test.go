package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
)

const statsUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// doStatsLogin performs a successful login carrying UA / language / screen
// headers and returns the response.
func doStatsLogin(h *testHarness, t *testing.T) *http.Response {
	t.Helper()
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

	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"email":"ivan@example.com","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", statsUA)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	req.Header.Set("X-Screen-Resolution", "1920x1080")
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	resp, err := h.app.Test(req, 1000)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func TestLoginRecordsStatisticsSuccess(t *testing.T) {
	h := buildHarness(t)
	resp := doStatsLogin(h, t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(h.queries.insertedStats) != 1 {
		t.Fatalf("expected 1 stats row, got %d", len(h.queries.insertedStats))
	}
	row := h.queries.insertedStats[0]
	if row.AuthError.String != "" {
		t.Errorf("expected empty auth_error, got %q", row.AuthError.String)
	}
	if !row.UserID.Valid || row.UserID.Bytes != fakeUser.ID.Bytes {
		t.Errorf("expected user id to be recorded")
	}
	if row.Browser.String != "Chrome" {
		t.Errorf("expected browser Chrome, got %q", row.Browser.String)
	}
	if row.Os.String != "macOS" {
		t.Errorf("expected os macOS, got %q", row.Os.String)
	}
	if row.Device.String != "Desktop" {
		t.Errorf("expected device Desktop, got %q", row.Device.String)
	}
	if row.ScreenResolution.String != "1920x1080" {
		t.Errorf("expected screen 1920x1080, got %q", row.ScreenResolution.String)
	}
	if row.Language.String != "ru-RU" {
		t.Errorf("expected language ru-RU, got %q", row.Language.String)
	}
	if row.UserAgent.String != statsUA {
		t.Errorf("expected UA to be stored")
	}
	if !row.LoginAt.Valid {
		t.Errorf("expected login_at to be set")
	}
}

func TestLoginRecordsInvalidCredentials(t *testing.T) {
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

	resp, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"wrong"}`)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	if len(h.queries.insertedStats) != 1 {
		t.Fatalf("expected 1 stats row, got %d", len(h.queries.insertedStats))
	}
	row := h.queries.insertedStats[0]
	if row.AuthError.String != "invalid_credentials" {
		t.Errorf("expected auth_error invalid_credentials, got %q", row.AuthError.String)
	}
	if !row.UserID.Valid {
		t.Errorf("expected user id to be recorded on failed login")
	}
}

func TestLoginRecordsUnknownEmail(t *testing.T) {
	h := buildHarness(t)
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return db.User{}, pgx.ErrNoRows
	}

	resp, err := h.call(http.MethodPost, "/login", `{"email":"nobody@example.com","password":"x"}`)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	if len(h.queries.insertedStats) != 1 {
		t.Fatalf("expected 1 stats row, got %d", len(h.queries.insertedStats))
	}
	row := h.queries.insertedStats[0]
	if row.AuthError.String != "user_not_found" {
		t.Errorf("expected auth_error user_not_found, got %q", row.AuthError.String)
	}
	if row.UserID.Valid {
		t.Errorf("expected no user id for unknown email")
	}
}

func TestLogoutUpdatesStatisticsDuration(t *testing.T) {
	h := buildHarness(t)
	resp := doStatsLogin(h, t)

	var body struct {
		User userResponse `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatalf("login did not set session cookie")
	}

	out, err := h.call(http.MethodPost, "/logout", "", cookie)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", out.StatusCode)
	}

	if len(h.queries.durationUpdates) != 1 {
		t.Fatalf("expected 1 duration update, got %d", len(h.queries.durationUpdates))
	}
	if h.queries.durationUpdates[0].Bytes != fakeUser.ID.Bytes {
		t.Errorf("expected duration update for logged in user")
	}
}

func TestStatsEndpointRequiresAdmin(t *testing.T) {
	app := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: &fakeSessionStore{sessions: map[string]string{"sess_user": fakeUser.ID.String()}},
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})

	resp, err := doRequestWithApp(t, app, http.MethodGet, "/stats", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous, got %d", resp.StatusCode)
	}

	resp, err = doRequestWithApp(t, app, http.MethodGet, "/stats", "",
		[]*http.Cookie{{Name: CookieName, Value: "sess_user"}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for regular user, got %d", resp.StatusCode)
	}
}

func TestStatsEndpointListsRowsForAdmin(t *testing.T) {
	fq := &fakeQuerier{}
	fq.getStatsFn = func(ctx context.Context, arg db.GetStatisticsParams) ([]db.Statistic, error) {
		return []db.Statistic{
			{
				ID:        42,
				UserID:    fakeUser.ID,
				Browser:   pgtype.Text{String: "Chrome", Valid: true},
				Device:    pgtype.Text{String: "Desktop", Valid: true},
				LoginAt:   pgtype.Timestamptz{Valid: true},
				AuthError: pgtype.Text{},
			},
		}, nil
	}

	resp, err := doRequestAdmin(t, fq, http.MethodGet, "/stats?limit=10&offset=0", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Items  []statsItem `json:"items"`
		Limit  int         `json:"limit"`
		Offset int         `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(body.Items))
	}
	item := body.Items[0]
	if item.ID != 42 || item.Browser != "Chrome" {
		t.Errorf("unexpected item: %+v", item)
	}
	if item.UserID != fakeUser.ID.String() {
		t.Errorf("expected user id %s, got %s", fakeUser.ID.String(), item.UserID)
	}
	if body.Limit != 10 || body.Offset != 0 {
		t.Errorf("unexpected pagination: %+v", body)
	}
}

func TestStatsEndpointRejectsInvalidUserFilter(t *testing.T) {
	resp, err := doRequestAdmin(t, &fakeQuerier{}, http.MethodGet, "/stats?user_id=not-a-uuid", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
