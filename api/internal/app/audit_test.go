package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
)

// loginWithUser performs a successful login and returns the session cookie.
func loginWithUser(h *testHarness, t *testing.T, user db.User, plain string) *http.Cookie {
	t.Helper()
	hasher := password.New()
	hash, err := hasher.Hash(plain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user.PasswordHash = pgtype.Text{String: hash, Valid: true}
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return user, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"email":"`+user.Email+`","password":"`+plain+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "audit-test-agent")
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	resp, err := h.app.Test(req, 1000)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("login did not set session cookie")
	return nil
}

func lastAudit(h *testHarness) db.InsertAuditLogParams {
	return h.queries.auditLogs[len(h.queries.auditLogs)-1]
}

func TestAuditRecordsLoginSuccess(t *testing.T) {
	h := buildHarness(t)
	loginWithUser(h, t, fakeUser, "correct-password")

	if len(h.queries.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != auditLoginSuccess {
		t.Errorf("expected action %s, got %q", auditLoginSuccess, row.Action)
	}
	if row.UserID.Bytes != fakeUser.ID.Bytes {
		t.Errorf("expected actor user id")
	}
	if row.UserAgent.String != "audit-test-agent" {
		t.Errorf("expected UA to be recorded, got %q", row.UserAgent.String)
	}
	if row.Ip == nil || !row.Ip.IsValid() {
		t.Errorf("expected IP to be recorded")
	}
}

func TestAuditRecordsLoginFailure(t *testing.T) {
	h := buildHarness(t)
	h.queries.getByEmailFn = func(ctx context.Context, email string) (db.User, error) {
		return fakeUser, nil
	}
	if _, err := h.call(http.MethodPost, "/login", `{"email":"ivan@example.com","password":"x"}`); err != nil {
		t.Fatalf("login: %v", err)
	}

	if len(h.queries.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != auditLoginFailed {
		t.Errorf("expected action %s, got %q", auditLoginFailed, row.Action)
	}
	var details map[string]any
	if err := json.Unmarshal(row.Details, &details); err != nil {
		t.Fatalf("details: %v", err)
	}
	if details["auth_error"] != "invalid_credentials" {
		t.Errorf("expected auth_error in details, got %v", details)
	}
}

func TestAuditRecordsLogout(t *testing.T) {
	h := buildHarness(t)
	cookie := loginWithUser(h, t, fakeUser, "correct-password")

	out, err := h.call(http.MethodPost, "/logout", "", cookie)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", out.StatusCode)
	}

	if len(h.queries.auditLogs) != 2 {
		t.Fatalf("expected 2 audit rows, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != auditLogout {
		t.Errorf("expected action %s, got %q", auditLogout, row.Action)
	}
}

func TestAuditRecordsConsent(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/consent",
		`{"accepted":true,"document_version":"1.0"}`, nil)
	if err != nil {
		t.Fatalf("consent: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	if len(h.queries.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != auditConsent {
		t.Errorf("expected action %s, got %q", auditConsent, row.Action)
	}
	if row.EntityID.String != "1.0" {
		t.Errorf("expected entity_id 1.0, got %q", row.EntityID.String)
	}
	if row.UserID.Valid {
		t.Errorf("expected anonymous consent (no actor)")
	}
}

func TestAuditRecordsRegistration(t *testing.T) {
	h := buildHarness(t)
	resp, err := h.call(http.MethodPost, "/registration",
		`{"first_name":"Анна","last_name":"Петрова","email":"anna@example.com","telegram":"@petrova","department":"IT","position":"dev","reason":"нужен доступ"}`, nil)
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	if len(h.queries.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != auditRegistration {
		t.Errorf("expected action %s, got %q", auditRegistration, row.Action)
	}
	if row.UserID.Valid {
		t.Errorf("expected anonymous registration (no actor)")
	}
}

func TestAuditRecordsUserOps(t *testing.T) {
	fq := &fakeQuerier{}

	createResp, err := doRequestAdmin(t, fq, http.MethodPost, "/users",
		`{"first_name":"Анна","last_name":"Петрова","email":"anna@example.com"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createResp.StatusCode)
	}

	updateResp, err := doRequestAdmin(t, fq, http.MethodPatch, "/users/"+fakeUser.ID.String(),
		`{"role":"admin"}`, adminCookies(t))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", updateResp.StatusCode)
	}

	deleteResp, err := doRequestAdmin(t, fq, http.MethodDelete, "/users/"+fakeUser.ID.String(), "", adminCookies(t))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", deleteResp.StatusCode)
	}

	if len(fq.auditLogs) != 3 {
		t.Fatalf("expected 3 audit rows, got %d", len(fq.auditLogs))
	}
	actions := []string{fq.auditLogs[0].Action, fq.auditLogs[1].Action, fq.auditLogs[2].Action}
	want := []string{auditUserCreated, auditUserUpdated, auditUserDeleted}
	for i, a := range want {
		if actions[i] != a {
			t.Errorf("action[%d]: expected %s, got %s", i, a, actions[i])
		}
	}
	for _, row := range fq.auditLogs {
		if row.UserID.Bytes != fakeAdmin.ID.Bytes {
			t.Errorf("expected admin actor for %s", row.Action)
		}
	}
}

func TestAuditRecordsBotCredentials(t *testing.T) {
	h := buildHarness(t)
	h.queries.getByTelegramFn = func(ctx context.Context, telegram pgtype.Text) (db.User, error) {
		u := fakeUser
		u.Telegram = pgtype.Text{String: "ivanov", Valid: true}
		u.Email = "ivan@example.com"
		return u, nil
	}
	h.app = New(&Server{
		Queries:  h.queries,
		Sessions: h.sessions,
		Hasher:   testHasher(),
		Notifier: h.notifier,
		Config:   Config{SessionTTL: time.Hour, BotToken: "bot-secret", BotEnabled: true, GatewayURL: "https://gateway.example.com"},
	}, HealthCheckers{})

	req := httptest.NewRequest(http.MethodPost, "/bot/credentials",
		strings.NewReader(`{"telegram_user_id":12345}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer bot-secret")
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
	resp, err := h.app.Test(req, 1000)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(h.queries.auditLogs) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(h.queries.auditLogs))
	}
	row := lastAudit(h)
	if row.Action != "password.reset_requested" {
		t.Errorf("expected action %s, got %q", auditCredentials, row.Action)
	}
	if row.UserID.Bytes != fakeUser.ID.Bytes {
		t.Errorf("expected actor to be the issued user")
	}
}

func TestLogsEndpointRequiresAdmin(t *testing.T) {
	app := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: &fakeSessionStore{sessions: map[string]string{"sess_user": fakeUser.ID.String()}},
		Hasher:   testHasher(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, HealthCheckers{})

	resp, err := doRequestWithApp(t, app, http.MethodGet, "/logs", "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous, got %d", resp.StatusCode)
	}

	resp, err = doRequestWithApp(t, app, http.MethodGet, "/logs", "",
		[]*http.Cookie{{Name: CookieName, Value: "sess_user"}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for regular user, got %d", resp.StatusCode)
	}
}

func TestLogsEndpointListsWithFilters(t *testing.T) {
	fq := &fakeQuerier{}
	var gotParams db.ListAuditLogsParams
	fq.getLogsFn = func(ctx context.Context, arg db.ListAuditLogsParams) ([]db.AuditLog, error) {
		gotParams = arg
		return []db.AuditLog{
			{
				ID:         7,
				Action:     auditLoginSuccess,
				EntityType: pgtype.Text{String: "user", Valid: true},
				UserAgent:  pgtype.Text{String: "ua", Valid: true},
				CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		}, nil
	}

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	resp, err := doRequestAdmin(t, fq, http.MethodGet,
		"/logs?limit=10&offset=5&action=login.success&entity=user&from="+from.Format(time.RFC3339), "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if gotParams.Limit != 10 || gotParams.Offset != 5 {
		t.Errorf("unexpected pagination: %+v", gotParams)
	}
	if gotParams.Column2 != "login.success" || gotParams.Column3 != "user" {
		t.Errorf("unexpected filters: %+v", gotParams)
	}
	if !gotParams.Column4.Valid || !gotParams.Column4.Time.Equal(from) {
		t.Errorf("expected from filter, got %+v", gotParams.Column4)
	}

	var body struct {
		Items  []auditItem `json:"items"`
		Limit  int         `json:"limit"`
		Offset int         `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(body.Items))
	}
	if body.Items[0].Action != auditLoginSuccess || body.Items[0].ID != 7 {
		t.Errorf("unexpected item: %+v", body.Items[0])
	}
}

func TestLogsEndpointRejectsInvalidParams(t *testing.T) {
	fq := &fakeQuerier{}

	resp, err := doRequestAdmin(t, fq, http.MethodGet, "/logs?actor=not-a-uuid", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid actor, got %d", resp.StatusCode)
	}

	resp, err = doRequestAdmin(t, fq, http.MethodGet, "/logs?from=not-a-date", "", adminCookies(t))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid from, got %d", resp.StatusCode)
	}
}
