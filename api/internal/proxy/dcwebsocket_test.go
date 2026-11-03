package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSecureDCRejectsUnauthorizedAndCrossOrigin(t *testing.T) {
	h, sid := gatedFixture(t, "https://web.telegram.org")
	h.SecureDC = true
	dialed := false
	h.dcDial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, fmt.Errorf("unexpected dial")
	}
	for _, tc := range []struct {
		sid, origin, path string
		status            int
	}{
		{"", "https://gateway.test", "/tg/_gw/dc/1/apiws", 401},
		{sid, "https://attacker.test", "/tg/_gw/dc/1/apiws", 403},
		{sid, "", "/tg/_gw/dc/1/apiws", 403},
		{sid, "https://gateway.test", "/tg/_gw/dc/6/apiws", 404},
		{sid, "https://gateway.test", "/tg/_gw/dc/1/other", 404},
	} {
		req := httptest.NewRequest("GET", "https://gateway.test"+tc.path, nil)
		if tc.sid != "" {
			req.AddCookie(&http.Cookie{Name: "tg_session", Value: tc.sid})
		}
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Origin", tc.origin)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		require.Equal(t, tc.status, recorder.Code)
	}
	require.False(t, dialed)
}
func TestSecureDCForwardsWithoutSecretsAndClosesOnRevoke(t *testing.T) {
	seen := make(chan http.Header, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		_, _ = io.Copy(conn, buf)
	}))
	defer upstream.Close()
	h, sid := gatedFixture(t, "https://web.telegram.org")
	h.SecureDC = true
	h.dcCheckInterval = 10 * time.Millisecond
	h.dcDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "kws1.web.telegram.org:443" {
			return nil, fmt.Errorf("unexpected destination %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	h.dcHandshake = func(ctx context.Context, c net.Conn, host string) (net.Conn, error) {
		return chromeTLS(ctx, c, host, true)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	host := strings.TrimPrefix(server.URL, "http://")
	_, err = fmt.Fprintf(conn, "GET /tg/_gw/dc/1/apiws HTTP/1.1\r\nHost: %s\r\nOrigin: https://%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nCookie: tg_session=%s; tg_csrf=secret\r\nAuthorization: Bearer private\r\nX-CSRF-Token: secret\r\n\r\n", host, host, sid)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, 101, res.StatusCode)
	headers := <-seen
	require.Empty(t, headers.Get("Cookie"))
	require.Empty(t, headers.Get("Authorization"))
	require.Empty(t, headers.Get("X-CSRF-Token"))
	require.Empty(t, headers.Get("X-Forwarded-For"))
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	got := make([]byte, 4)
	_, err = io.ReadFull(reader, got)
	require.NoError(t, err)
	require.Equal(t, "ping", string(got))
	require.NoError(t, h.Sessions.Delete(t.Context(), sid))
	_, err = reader.ReadByte()
	require.Error(t, err)
	if ne, ok := err.(net.Error); ok {
		require.False(t, ne.Timeout(), "revocation must close the connection")
	}
}
func TestSecureBootstrapUsesSessionOrigin(t *testing.T) {
	body, ok, err := rewriteBootstrapBundle([]byte(bootstrapSnippet), DC{SameOrigin: true})
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, string(body), "wss://${new URL(self.location.origin).host}/tg/_gw/dc/${r}/${a}")
}

func TestSecureWorkerFailsClosedOnMissAndOversize(t *testing.T) {
	for _, body := range [][]byte{[]byte("unknown worker shape"), bytes.Repeat([]byte("x"), maxBundleBody+2)} {
		_, _, _, err := rewriteBundleBody(io.NopCloser(bytes.NewReader(body)), DC{SameOrigin: true})
		require.Error(t, err)
	}
}
