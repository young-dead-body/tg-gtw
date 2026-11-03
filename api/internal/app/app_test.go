package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/session"
)

const testCSRFToken = "test-csrf-token-0123456789abcdef"

// addTestCSRF attaches the standard double-submit token to state-changing
// test requests unless the caller already provided one (CSRF tests rely on
// this to simulate missing/mismatched tokens).
func addTestCSRF(method string, req *http.Request, cookies []*http.Cookie) []*http.Cookie {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return cookies
	}
	if req.Header.Get("X-CSRF-Token") != "" || hasCookie(cookies, req, CSRFCookieName) {
		return cookies
	}
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	return append(cookies, &http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
}

func hasCookie(cookies []*http.Cookie, req *http.Request, name string) bool {
	for _, c := range cookies {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range req.Cookies() {
		if c.Name == name {
			return true
		}
	}
	return false
}

func doRequest(t *testing.T, checkers HealthCheckers, method, path string, body string, cookies []*http.Cookie) (*http.Response, error) {
	t.Helper()
	application := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: &fakeSessionStore{},
		Hasher:   password.New(),
		Notifier: &fakeNotifier{},
		Config:   Config{SessionTTL: time.Hour},
	}, checkers)

	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	cookies = addTestCSRF(method, req, cookies)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return application.Test(req, 1000)
}

func okProbe(context.Context) error { return nil }

func failProbe(context.Context) error { return errors.New("down") }

func decodeHealth(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestHealthAllOK(t *testing.T) {
	resp, err := doRequest(t, HealthCheckers{Postgres: okProbe, Redis: okProbe}, http.MethodGet, "/health", "", nil)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeHealth(t, resp)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
	checks := body["checks"].(map[string]any)
	if checks["postgres"].(map[string]any)["status"] != "ok" {
		t.Errorf("expected postgres=ok, got %v", checks["postgres"])
	}
	if checks["redis"].(map[string]any)["status"] != "ok" {
		t.Errorf("expected redis=ok, got %v", checks["redis"])
	}
}

func TestHealthDegradedOnRedisFailure(t *testing.T) {
	resp, err := doRequest(t, HealthCheckers{Postgres: okProbe, Redis: failProbe}, http.MethodGet, "/health", "", nil)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	body := decodeHealth(t, resp)
	if body["status"] != "degraded" {
		t.Errorf("expected status=degraded, got %v", body["status"])
	}
	c := body["checks"].(map[string]any)
	if c["redis"].(map[string]any)["status"] != "error" {
		t.Errorf("expected redis=error, got %v", c["redis"])
	}
}

func TestHealthWithoutProbes(t *testing.T) {
	resp, err := doRequest(t, HealthCheckers{}, http.MethodGet, "/health", "", nil)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHealthInvalidMethod(t *testing.T) {
	resp, err := doRequest(t, HealthCheckers{Postgres: okProbe}, http.MethodPost, "/health", "", nil)
	if err != nil {
		t.Fatalf("app test: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// --- fakes ---

var fakeUser = db.User{
	ID:        pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	FirstName: "Иван",
	LastName:  "Иванов",
	Email:     "ivan@example.com",
	Role:      "user",
	IsActive:  true,
}

type fakeQuerier struct {
	mu sync.Mutex
	db.Querier
	tokens            map[string]db.IssueOneTimeTokenParams
	telegramID        int64
	insertedConsents  []db.InsertConsentParams
	linkedReceipts    []db.LinkConsentToUserParams
	getByTelegramIDFn func(context.Context, int64) (db.User, error)

	getByEmailFn         func(ctx context.Context, email string) (db.User, error)
	getByIDFn            func(ctx context.Context, id pgtype.UUID) (db.User, error)
	getByTelegramFn      func(ctx context.Context, telegram pgtype.Text) (db.User, error)
	listRecentFn         func(ctx context.Context, arg db.ListRecentPasswordHashesParams) ([]string, error)
	createRegistrationFn func(ctx context.Context, arg db.CreateRegistrationRequestParams) (db.RegistrationRequest, error)
	listRegistrationFn   func(ctx context.Context) ([]db.RegistrationRequest, error)
	updateRegistrationFn func(ctx context.Context, arg db.UpdateRegistrationRequestStatusParams) (db.RegistrationRequest, error)
	getRegistrationFn    func(ctx context.Context, id pgtype.UUID) (db.RegistrationRequest, error)
	deleteUserFn         func(ctx context.Context, id pgtype.UUID) (db.User, error)
	getSettingFn         func(ctx context.Context, key string) (db.Setting, error)
	getStatsFn           func(ctx context.Context, arg db.GetStatisticsParams) ([]db.Statistic, error)
	getLogsFn            func(ctx context.Context, arg db.ListAuditLogsParams) ([]db.AuditLog, error)
	insertedStats        []db.InsertStatisticsParams
	durationUpdates      []pgtype.UUID
	auditLogs            []db.InsertAuditLogParams
	createdUsers         []db.CreateUserParams
	hasConsent           *bool
}

func (f *fakeQuerier) GetUserByEmail(ctx context.Context, email string) (db.User, error) {
	if f.getByEmailFn != nil {
		return f.getByEmailFn(ctx, email)
	}
	return fakeUser, nil
}

func (f *fakeQuerier) GetUserByID(ctx context.Context, id pgtype.UUID) (db.User, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(ctx, id)
	}
	if id == fakeAdmin.ID {
		return fakeAdmin, nil
	}
	return fakeUser, nil
}

func (f *fakeQuerier) CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error) {
	f.createdUsers = append(f.createdUsers, arg)
	return fakeUser, nil
}

func (f *fakeQuerier) ListUsers(ctx context.Context) ([]db.User, error) {
	return []db.User{fakeUser}, nil
}

func (f *fakeQuerier) GetSetting(ctx context.Context, key string) (db.Setting, error) {
	if f.getSettingFn != nil {
		return f.getSettingFn(ctx, key)
	}
	return db.Setting{}, pgx.ErrNoRows
}

func (f *fakeQuerier) SetSetting(ctx context.Context, arg db.SetSettingParams) error {
	return nil
}

func (f *fakeQuerier) UpdateUserPassword(ctx context.Context, arg db.UpdateUserPasswordParams) (db.User, error) {
	return fakeUser, nil
}

func (f *fakeQuerier) InsertPasswordHistory(ctx context.Context, arg db.InsertPasswordHistoryParams) (db.PasswordHistory, error) {
	return db.PasswordHistory{}, nil
}

func (f *fakeQuerier) ListRecentPasswordHashes(ctx context.Context, arg db.ListRecentPasswordHashesParams) ([]string, error) {
	if f.listRecentFn != nil {
		return f.listRecentFn(ctx, arg)
	}
	return []string{}, nil
}

func (f *fakeQuerier) CreateRegistrationRequest(ctx context.Context, arg db.CreateRegistrationRequestParams) (db.RegistrationRequest, error) {
	if f.createRegistrationFn != nil {
		return f.createRegistrationFn(ctx, arg)
	}
	return db.RegistrationRequest{
		ID:        pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		FirstName: arg.FirstName,
		LastName:  arg.LastName,
		Email:     arg.Email,
		Status:    "pending",
	}, nil
}

func (f *fakeQuerier) ListRegistrationRequests(ctx context.Context) ([]db.RegistrationRequest, error) {
	if f.listRegistrationFn != nil {
		return f.listRegistrationFn(ctx)
	}
	return []db.RegistrationRequest{}, nil
}

func (f *fakeQuerier) GetRegistrationRequest(ctx context.Context, id pgtype.UUID) (db.RegistrationRequest, error) {
	if f.getRegistrationFn != nil {
		return f.getRegistrationFn(ctx, id)
	}
	return db.RegistrationRequest{ID: id, Status: "pending"}, nil
}

func (f *fakeQuerier) UpdateRegistrationRequestStatus(ctx context.Context, arg db.UpdateRegistrationRequestStatusParams) (db.RegistrationRequest, error) {
	if f.updateRegistrationFn != nil {
		return f.updateRegistrationFn(ctx, arg)
	}
	return db.RegistrationRequest{ID: arg.ID, Status: arg.Status}, nil
}

func (f *fakeQuerier) UpdateUserProfile(ctx context.Context, arg db.UpdateUserProfileParams) (db.User, error) {
	return fakeUser, nil
}

func (f *fakeQuerier) DeleteUser(ctx context.Context, id pgtype.UUID) (db.User, error) {
	if f.deleteUserFn != nil {
		return f.deleteUserFn(ctx, id)
	}
	return fakeUser, nil
}

func (f *fakeQuerier) TouchPasswordChangedAt(ctx context.Context, id pgtype.UUID) error {
	return nil
}

func (f *fakeQuerier) InsertConsent(ctx context.Context, arg db.InsertConsentParams) (db.Consent, error) {
	f.insertedConsents = append(f.insertedConsents, arg)
	return db.Consent{
		ID:              pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		DocumentVersion: arg.DocumentVersion,
		AcceptedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}, nil
}

func (f *fakeQuerier) HasUserConsent(ctx context.Context, userID pgtype.UUID) (bool, error) {
	if f.hasConsent != nil {
		return *f.hasConsent, nil
	}
	return true, nil
}

func (f *fakeQuerier) LinkConsentToUser(ctx context.Context, arg db.LinkConsentToUserParams) error {
	f.linkedReceipts = append(f.linkedReceipts, arg)
	return nil
}

func (f *fakeQuerier) GetUserByTelegram(ctx context.Context, telegram pgtype.Text) (db.User, error) {
	if f.getByTelegramFn != nil {
		return f.getByTelegramFn(ctx, telegram)
	}
	return fakeUser, nil
}

func (f *fakeQuerier) SetUserPassword(ctx context.Context, arg db.SetUserPasswordParams) (db.User, error) {
	return fakeUser, nil
}

func (f *fakeQuerier) ListUsersWithExpiry(ctx context.Context) ([]db.User, error) {
	return []db.User{fakeUser}, nil
}

func (f *fakeQuerier) InsertProxyPool(ctx context.Context, arg db.InsertProxyPoolParams) (db.ProxyPool, error) {
	return db.ProxyPool{}, nil
}

func (f *fakeQuerier) ListActiveProxies(ctx context.Context) ([]db.ProxyPool, error) {
	return nil, nil
}

func (f *fakeQuerier) ListProxies(ctx context.Context) ([]db.ProxyPool, error) {
	return nil, nil
}

func (f *fakeQuerier) UpdateProxyHealth(ctx context.Context, arg db.UpdateProxyHealthParams) error {
	return nil
}

func (f *fakeQuerier) CountUsers(ctx context.Context) (int64, error) {
	return 12, nil
}

func (f *fakeQuerier) CountBlockedUsers(ctx context.Context) (int64, error) {
	return 1, nil
}

func (f *fakeQuerier) CountPendingRegistrationRequests(ctx context.Context) (int64, error) {
	return 2, nil
}

func (f *fakeQuerier) CountSuccessfulLoginsSince(ctx context.Context, since pgtype.Timestamptz) (int64, error) {
	return 5, nil
}

func (f *fakeQuerier) CountLoginsTotal(ctx context.Context) (int64, error) {
	return 42, nil
}

func (f *fakeQuerier) InsertStatistics(ctx context.Context, arg db.InsertStatisticsParams) (db.Statistic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertedStats = append(f.insertedStats, arg)
	return db.Statistic{ID: int64(len(f.insertedStats))}, nil
}

func (f *fakeQuerier) UpdateStatisticsDuration(ctx context.Context, userID pgtype.UUID) error {
	f.durationUpdates = append(f.durationUpdates, userID)
	return nil
}

func (f *fakeQuerier) GetStatistics(ctx context.Context, arg db.GetStatisticsParams) ([]db.Statistic, error) {
	if f.getStatsFn != nil {
		return f.getStatsFn(ctx, arg)
	}
	return []db.Statistic{}, nil
}

func (f *fakeQuerier) InsertAuditLog(ctx context.Context, arg db.InsertAuditLogParams) (db.AuditLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auditLogs = append(f.auditLogs, arg)
	return db.AuditLog{ID: int64(len(f.auditLogs))}, nil
}

func (f *fakeQuerier) ListAuditLogs(ctx context.Context, arg db.ListAuditLogsParams) ([]db.AuditLog, error) {
	if f.getLogsFn != nil {
		return f.getLogsFn(ctx, arg)
	}
	return []db.AuditLog{}, nil
}

func (f *fakeQuerier) ArchiveOldAuditLogs(ctx context.Context, createdAt pgtype.Timestamptz) error {
	return nil
}

func (f *fakeQuerier) CountOldAuditLogs(ctx context.Context, createdAt pgtype.Timestamptz) (int64, error) {
	return 0, nil
}

func (f *fakeQuerier) DeleteArchivedLogs(ctx context.Context) error {
	return nil
}

func (f *fakeQuerier) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	return 0, nil
}

func (f *fakeQuerier) DeleteOldRegistrationRequests(ctx context.Context, secs float64) (int64, error) {
	return 0, nil
}

type fakeNotifier struct {
	events map[string][]string
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{events: map[string][]string{}}
}

func (n *fakeNotifier) Notify(ctx context.Context, event string, payload string) error {
	n.events[event] = append(n.events[event], payload)
	return nil
}

type fakeSessionStore struct {
	sessions map[string]string
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: map[string]string{}}
}

func (f *fakeSessionStore) Create(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	if f.sessions == nil {
		f.sessions = map[string]string{}
	}
	id := "sess_" + userID
	f.sessions[id] = userID
	return id, nil
}

func (f *fakeSessionStore) Get(ctx context.Context, id string) (string, error) {
	if v, ok := f.sessions[id]; ok {
		return v, nil
	}
	return "", session.ErrNotFound
}

func (f *fakeSessionStore) Delete(ctx context.Context, id string) error {
	delete(f.sessions, id)
	return nil
}

func (f *fakeSessionStore) DeleteUser(ctx context.Context, userID string) error {
	for id, uid := range f.sessions {
		if uid == userID {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeQuerier) IssueOneTimeToken(_ context.Context, arg db.IssueOneTimeTokenParams) error {
	if f.tokens == nil {
		f.tokens = map[string]db.IssueOneTimeTokenParams{}
	}
	for key, old := range f.tokens {
		if old.UserID == arg.UserID && old.Purpose == arg.Purpose {
			delete(f.tokens, key)
		}
	}
	f.tokens[arg.TokenHash] = arg
	return nil
}
func (f *fakeQuerier) GetUserByTelegramID(ctx context.Context, id int64) (db.User, error) {
	if f.getByTelegramIDFn != nil {
		return f.getByTelegramIDFn(ctx, id)
	}
	return fakeUser, nil
}
func (f *fakeQuerier) GetTelegramBinding(context.Context, pgtype.UUID) (db.TelegramBinding, error) {
	return db.TelegramBinding{UserID: fakeUser.ID, TelegramUserID: f.telegramID}, nil
}
func (f *fakeQuerier) BindTelegram(_ context.Context, arg db.BindTelegramParams) (db.TelegramBinding, error) {
	token, ok := f.tokens[arg.TokenHash]
	if !ok || token.Purpose != "telegram_link" || !token.ExpiresAt.Time.After(time.Now()) {
		return db.TelegramBinding{}, pgx.ErrNoRows
	}
	delete(f.tokens, arg.TokenHash)
	f.telegramID = arg.TelegramUserID
	return db.TelegramBinding{UserID: token.UserID, TelegramUserID: arg.TelegramUserID}, nil
}
func (f *fakeQuerier) GetPasswordResetUser(_ context.Context, digest string) (db.User, error) {
	token, ok := f.tokens[digest]
	if !ok || token.Purpose != "password_reset" || !token.ExpiresAt.Time.After(time.Now()) {
		return db.User{}, pgx.ErrNoRows
	}
	return fakeUser, nil
}
func (f *fakeQuerier) RedeemPasswordReset(ctx context.Context, arg db.RedeemPasswordResetParams) (db.User, error) {
	u, err := f.GetPasswordResetUser(ctx, arg.TokenHash)
	if err != nil {
		return db.User{}, err
	}
	delete(f.tokens, arg.TokenHash)
	return u, nil
}

func (f *fakeQuerier) IssuePasswordResetToken(ctx context.Context, arg db.IssuePasswordResetTokenParams) (db.OneTimeToken, error) {
	u, err := f.GetUserByTelegramID(ctx, arg.TelegramUserID)
	if err != nil {
		return db.OneTimeToken{}, err
	}
	err = f.IssueOneTimeToken(ctx, db.IssueOneTimeTokenParams{UserID: u.ID, Purpose: "password_reset", TokenHash: arg.TokenHash, ExpiresAt: arg.ExpiresAt})
	return db.OneTimeToken{UserID: u.ID, Purpose: "password_reset", TokenHash: arg.TokenHash, ExpiresAt: arg.ExpiresAt}, err
}
