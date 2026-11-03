package proxy

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func turnFixture(t *testing.T, upstreamURL string) (*Handler, string) {
	t.Helper()
	handler, sid := gatedFixture(t, upstreamURL)
	handler.TURN = &TURN{
		URLs:   []string{"turns:turn.example.test:443?transport=tcp"},
		Secret: "s3cret",
		TTL:    time.Hour,
	}
	return handler, sid
}

func htmlUpstream(t *testing.T, body string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTURNCredentialsForSession(t *testing.T) {
	hits := 0
	handler, sid := turnFixture(t, htmlUpstream(t, "", &hits).URL)

	resp := gatedRequest(handler, sid, http.MethodGet, gatewayTURNPath, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	require.Zero(t, hits, "gateway endpoints must not reach Telegram")

	var got turnCredentials
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.EqualValues(t, 3600, got.TTL)
	require.Len(t, got.IceServers, 1)
	ice := got.IceServers[0]
	require.Equal(t, []string{"turns:turn.example.test:443?transport=tcp"}, ice.URLs)

	// TURN REST format: "<expiry>:<user id>", HMAC-SHA1 password.
	expiry, user, ok := strings.Cut(ice.Username, ":")
	require.True(t, ok)
	require.Equal(t, uuidFromBytes(9).String(), user)
	exp, err := strconv.ParseInt(expiry, 10, 64)
	require.NoError(t, err)
	require.InDelta(t, time.Now().Add(time.Hour).Unix(), exp, 5)

	mac := hmac.New(sha1.New, []byte("s3cret"))
	mac.Write([]byte(ice.Username))
	require.Equal(t, base64.StdEncoding.EncodeToString(mac.Sum(nil)), ice.Credential)
}

func TestTURNCredentialsRequireSession(t *testing.T) {
	handler, _ := turnFixture(t, htmlUpstream(t, "", nil).URL)

	req := httptest.NewRequest(http.MethodGet, gatewayTURNPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGatewayScriptServed(t *testing.T) {
	handler, sid := turnFixture(t, htmlUpstream(t, "", nil).URL)

	resp := gatedRequest(handler, sid, http.MethodGet, gatewayScript, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "javascript")
	require.Contains(t, readBody(t, resp), "RTCPeerConnection")
}

func TestGatewayPathsNotFoundWithoutTURN(t *testing.T) {
	hits := 0
	handler, sid := gatedFixture(t, htmlUpstream(t, "", &hits).URL)

	for _, p := range []string{gatewayScript, gatewayTURNPath, gatewayPrefix + "other"} {
		resp := gatedRequest(handler, sid, http.MethodGet, p, nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, p)
	}
	require.Zero(t, hits)
}

func TestInjectsRTCScriptIntoHTML(t *testing.T) {
	page := `<!doctype html><html><head lang="ru"><script type="module" src="./index.js"></script></head><body></body></html>`
	handler, sid := turnFixture(t, htmlUpstream(t, page, nil).URL)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, `<head lang="ru"><script src="/tg/_gw/rtc.js"></script><script type="module"`)
	require.Equal(t, strconv.Itoa(len(body)), resp.Header.Get("Content-Length"))
}

func TestNoInjectionWithoutTURN(t *testing.T) {
	page := `<html><head></head></html>`
	handler, sid := gatedFixture(t, htmlUpstream(t, page, nil).URL)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/", nil)
	require.Equal(t, page, readBody(t, resp))
}

func TestNoInjectionIntoNonHTML(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("const head = '<head>';"))
	}))
	defer upstream.Close()
	handler, sid := turnFixture(t, upstream.URL)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/app.js", nil)
	require.Equal(t, "const head = '<head>';", readBody(t, resp))
}

func TestInsertAfterHead(t *testing.T) {
	tag := []byte("<x>")
	for in, want := range map[string]string{
		"<html><head><title>t</title></head>":  "<html><head><x><title>t</title></head>",
		"<HTML><HEAD>\n</HEAD>":                "<HTML><HEAD><x>\n</HEAD>",
		"<header>h</header><head id=a></head>": "<header>h</header><head id=a><x></head>",
		"<body>no head</body>":                 "<x><body>no head</body>",
	} {
		require.Equal(t, want, string(insertAfterHead([]byte(in), tag)), in)
	}
}
