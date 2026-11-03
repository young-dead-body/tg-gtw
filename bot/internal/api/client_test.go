package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueCredentials(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"reset_url":"https://gw.example.com/#reset=token","expires_in":900}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "svc-token")
	creds, err := c.IssueCredentials(context.Background(), "12345")

	require.NoError(t, err)
	require.Equal(t, "Bearer svc-token", gotAuth)
	require.Equal(t, "/bot/credentials", gotPath)
	require.EqualValues(t, 12345, gotBody["telegram_user_id"])
	require.Equal(t, "https://gw.example.com/#reset=token", creds.ResetURL)
	require.Equal(t, 900, creds.ExpiresIn)
}

func TestIssueCredentialsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"user not found by telegram handle"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	_, err := c.IssueCredentials(context.Background(), "12345")

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusNotFound, apiErr.Status)
}

func TestGetUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/bot/users/abc-123", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123","email":"ivan@example.com","first_name":"Иван",` +
			`"last_name":"Иванов","telegram":"@ivanov","is_active":true,"expires_at":"2026-09-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	u, err := c.GetUser(context.Background(), "abc-123")

	require.NoError(t, err)
	require.Equal(t, "@ivanov", u.Telegram)
	require.True(t, u.IsActive)
	require.Equal(t, "2026-09-01", u.ExpiresAt.Format("2006-01-02"))
}

func TestGetUserNoExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"1","telegram":"@ivanov"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	u, err := c.GetUser(context.Background(), "1")

	require.NoError(t, err)
	require.Nil(t, u.ExpiresAt)
}

func TestBaseURLAvoidsDoubleSlash(t *testing.T) {
	require.Equal(t, "http://x", NewClient("http://x/", "t").baseURL)
}
