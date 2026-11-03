package app

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/password"
)

const testBotToken = "test-bot-secret"

func buildBotHarness(t *testing.T, q *fakeQuerier) *testHarness {
	t.Helper()
	if q == nil {
		q = &fakeQuerier{}
	}
	store := newFakeSessionStore()
	notifier := newFakeNotifier()
	application := New(&Server{
		Queries:  q,
		Sessions: store,
		Hasher:   password.New(),
		Notifier: notifier,
		Config: Config{
			SessionTTL:           24 * 7 * 3600,
			PasswordReuseHistory: 5,
			BotToken:             testBotToken,
			BotEnabled:           true,
			GatewayURL:           "https://gateway.example.com",
		},
	}, HealthCheckers{})
	return &testHarness{app: application, sessions: store, queries: q, notifier: notifier}
}

func botRequest(h *testHarness, method, path, body string) (*http.Response, error) {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req, err := http.NewRequest(method, path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testBotToken)
	req.Host = "localhost"
	return h.app.Test(req, 1000)
}

func TestBotCredentialsRequiresToken(t *testing.T) {
	h := buildBotHarness(t, nil)
	resp, err := h.call(http.MethodPost, "/bot/credentials", `{"telegram_user_id":12345}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestBotCredentialsWrongToken(t *testing.T) {
	h := buildBotHarness(t, nil)
	req, err := http.NewRequest(http.MethodPost, "/bot/credentials", strings.NewReader(`{"telegram_user_id":12345}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	req.Host = "localhost"
	resp, err2 := h.app.Test(req, 1000)
	require.NoError(t, err2)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestBotCredentialsSuccess(t *testing.T) {
	h := buildBotHarness(t, nil)
	resp, err := botRequest(h, http.MethodPost, "/bot/credentials", `{"telegram_user_id":12345}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeBody(t, resp)
	require.NotContains(t, body, "password")
	link := body["reset_url"].(string)
	require.Contains(t, link, "https://gateway.example.com/#reset=")
	require.EqualValues(t, 900, body["expires_in"])
	token := strings.Split(link, "#reset=")[1]
	require.Contains(t, h.queries.tokens, tokenHash(token))
	require.Empty(t, h.notifier.events["password.changed"])

}

func TestBotCredentialsUserNotFound(t *testing.T) {
	q := &fakeQuerier{getByTelegramIDFn: func(_ context.Context, _ int64) (db.User, error) {
		return db.User{}, pgx.ErrNoRows
	}}
	h := buildBotHarness(t, q)
	resp, err := botRequest(h, http.MethodPost, "/bot/credentials", `{"telegram_user_id":12345}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBotCredentialsDisabledUser(t *testing.T) {
	disabled := fakeUser
	disabled.IsActive = false
	q := &fakeQuerier{getByTelegramIDFn: func(_ context.Context, _ int64) (db.User, error) {
		return disabled, nil
	}}
	h := buildBotHarness(t, q)
	resp, err := botRequest(h, http.MethodPost, "/bot/credentials", `{"telegram_user_id":12345}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestBotCredentialsMissingTelegram(t *testing.T) {
	h := buildBotHarness(t, nil)
	resp, err := botRequest(h, http.MethodPost, "/bot/credentials", `{"telegram_user_id":0}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBotUserResolve(t *testing.T) {
	u := fakeUser
	u.Telegram = pgtype.Text{String: "@alice", Valid: true}
	q := &fakeQuerier{getByIDFn: func(_ context.Context, _ pgtype.UUID) (db.User, error) { return u, nil }}
	h := buildBotHarness(t, q)
	resp, err := botRequest(h, http.MethodGet, "/bot/users/00000000-0000-0000-0000-000000000001", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeBody(t, resp)
	require.Equal(t, "ivan@example.com", body["email"])
	require.Equal(t, "@alice", body["telegram"])
	require.Equal(t, true, body["is_active"])
}

func TestBotUserNotFound(t *testing.T) {
	q := &fakeQuerier{getByIDFn: func(_ context.Context, _ pgtype.UUID) (db.User, error) {
		return db.User{}, pgx.ErrNoRows
	}}
	h := buildBotHarness(t, q)
	resp, err := botRequest(h, http.MethodGet, "/bot/users/00000000-0000-0000-0000-000000000001", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestBotEndpointDisabledWhenTokenEmpty(t *testing.T) {
	store := newFakeSessionStore()
	application := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: store,
		Hasher:   password.New(),
		Notifier: newFakeNotifier(),
		Config:   Config{SessionTTL: 3600, BotEnabled: true},
	}, HealthCheckers{})
	req, err := http.NewRequest(http.MethodPost, "/bot/credentials", strings.NewReader(`{"telegram_user_id":12345}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer whatever")
	req.Host = "localhost"
	resp, err := application.Test(req, 1000)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestBotRoutesNotRegisteredWhenDisabled verifies the /bot group is not
// mounted at all when the tg-bot feature toggle is off.
func TestBotRoutesNotRegisteredWhenDisabled(t *testing.T) {
	store := newFakeSessionStore()
	application := New(&Server{
		Queries:  &fakeQuerier{},
		Sessions: store,
		Hasher:   password.New(),
		Notifier: newFakeNotifier(),
		Config:   Config{SessionTTL: 3600, BotEnabled: false, BotToken: testBotToken},
	}, HealthCheckers{})
	for _, path := range []string{"/bot/credentials", "/bot/users/00000000-0000-0000-0000-000000000001"} {
		req, err := http.NewRequest(http.MethodPost, path, strings.NewReader(`{"telegram_user_id":12345}`))
		if err != nil {
			t.Fatalf("req: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testBotToken)
		req.Host = "localhost"
		resp, err := application.Test(req, 1000)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
}
