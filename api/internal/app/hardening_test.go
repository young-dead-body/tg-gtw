package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/company/telegram-gateway/api/internal/password"
	"github.com/company/telegram-gateway/api/internal/session"
	"github.com/redis/go-redis/v9"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestConsentRejectsStaleVersion(t *testing.T) {
	h := buildHarness(t)
	h.queries.getSettingFn = func(context.Context, string) (db.Setting, error) { return db.Setting{Value: []byte(`"2.0"`)}, nil }
	res, err := h.call("POST", "/consent", `{"accepted":true,"document_version":"1.0"}`)
	require.NoError(t, err)
	require.Equal(t, 409, res.StatusCode)
	require.Empty(t, h.queries.insertedConsents)
}
func TestConsentReceiptIsPrivateAndBoundByID(t *testing.T) {
	h := buildHarness(t)
	res, err := h.call("POST", "/consent", `{"accepted":true,"document_version":"1.0"}`)
	require.NoError(t, err)
	cookie := testCookie(t, res, consentReceiptCookie)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	hash, err := password.New().Hash("valid-password")
	require.NoError(t, err)
	h.queries.getByEmailFn = func(context.Context, string) (db.User, error) {
		u := fakeUser
		u.PasswordHash = pgtype.Text{String: hash, Valid: true}
		return u, nil
	}
	res, err = h.call("POST", "/login", `{"email":"ivan@example.com","password":"valid-password"}`, cookie)
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	require.Len(t, h.queries.linkedReceipts, 1)
	require.Equal(t, cookie.Value, h.queries.linkedReceipts[0].ConsentID.String())
}
func TestResetLinkIsSingleUseAndRevokesSessions(t *testing.T) {
	h := buildBotHarness(t, nil)
	old, err := h.sessions.Create(t.Context(), fakeUser.ID.String(), 3600000000000)
	require.NoError(t, err)
	res, err := botRequest(h, "POST", "/bot/credentials", `{"telegram_user_id":12345}`)
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	body := decodeBody(t, res)
	token := strings.Split(body["reset_url"].(string), "#reset=")[1]
	payload := `{"token":"` + token + `","password":"new-password"}`
	res, err = h.call("POST", "/password/reset", payload)
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	_, err = h.sessions.Get(t.Context(), old)
	require.Error(t, err)
	res, err = h.call("POST", "/password/reset", payload)
	require.NoError(t, err)
	require.Equal(t, 400, res.StatusCode)
}
func TestBotLinkRequiresReceiptAndStableID(t *testing.T) {
	h := buildBotHarness(t, nil)
	token, err := issueTestLink(h)
	require.NoError(t, err)
	payload := `{"code":"` + token + `","telegram_user_id":12345}`
	res, err := botRequest(h, "POST", "/bot/link", payload)
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	require.EqualValues(t, 12345, h.queries.telegramID)
	res, err = botRequest(h, "POST", "/bot/link", payload)
	require.NoError(t, err)
	require.Equal(t, 409, res.StatusCode)
}
func issueTestLink(h *testHarness) (string, error) {
	token := strings.Repeat("a", 64)
	return token, h.queries.IssueOneTimeToken(context.Background(), db.IssueOneTimeTokenParams{UserID: fakeUser.ID, Purpose: "telegram_link", TokenHash: tokenHash(token), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}})
}
func TestAnonymousCannotManageSessions(t *testing.T) {
	h := buildHarness(t)
	res, err := h.call(http.MethodGet, "/me/sessions", "")
	require.NoError(t, err)
	require.Equal(t, 401, res.StatusCode)
}

func TestSessionManagementNeverExposesBearerAndChecksOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	store := session.NewRedisStore(client)
	revision := session.CredentialRevision(fakeUser.PasswordHash.String, fakeUser.PasswordChangedAt.Time)
	sid, err := store.CreateVersioned(t.Context(), fakeUser.ID.String(), time.Hour, revision)
	require.NoError(t, err)
	other, err := store.CreateVersioned(t.Context(), "another-user", time.Hour, revision)
	require.NoError(t, err)
	q := &fakeQuerier{}
	app := New(&Server{Queries: q, Sessions: store, Hasher: password.New(), Notifier: newFakeNotifier()}, HealthCheckers{})
	request := func(method, path string) *http.Response {
		req := httptest.NewRequest(method, path, nil)
		for _, cookie := range addTestCSRF(method, req, []*http.Cookie{{Name: CookieName, Value: sid}}) {
			req.AddCookie(cookie)
		}
		response, err := app.Test(req)
		require.NoError(t, err)
		return response
	}
	res := request("GET", "/me/sessions")
	require.Equal(t, 200, res.StatusCode)
	rows := decodeBody(t, res)["sessions"].([]any)
	require.Len(t, rows, 1)
	info := rows[0].(map[string]any)
	require.NotEqual(t, sid, info["id"])
	require.Equal(t, session.PublicID(sid), info["id"])
	require.Equal(t, true, info["current"])
	res = request("DELETE", "/me/sessions/"+session.PublicID(other))
	require.Equal(t, 404, res.StatusCode)
	_, err = store.Get(t.Context(), other)
	require.NoError(t, err)
	// DB revision changes invalidate the old credential without deleting Redis.
	q.getByIDFn = func(context.Context, pgtype.UUID) (db.User, error) {
		u := fakeUser
		u.PasswordHash = pgtype.Text{String: "new-hash", Valid: true}
		return u, nil
	}
	res = request("GET", "/me")
	require.Equal(t, 401, res.StatusCode)
	_, err = store.Get(t.Context(), sid)
	require.NoError(t, err)
	q.getByIDFn = nil
	res = request("DELETE", "/me/sessions/"+session.PublicID(sid))
	require.Equal(t, 200, res.StatusCode)
	_, err = store.Get(t.Context(), sid)
	require.Error(t, err)
}

func TestExpiredAdminCannotAccessAdminAPI(t *testing.T) {
	h := buildHarness(t)
	u := fakeAdmin
	u.ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	h.queries.getByIDFn = func(context.Context, pgtype.UUID) (db.User, error) { return u, nil }
	sid, err := h.sessions.Create(t.Context(), u.ID.String(), time.Hour)
	require.NoError(t, err)
	res, err := h.call("GET", "/users", "", &http.Cookie{Name: CookieName, Value: sid})
	require.NoError(t, err)
	require.Equal(t, 403, res.StatusCode)
}
