package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/company/telegram-gateway/api/internal/db"
)

// bootstrapSnippet is the WebSocket URL constructor from the live Telegram Web
// K worker bundle (index.worker-*.js), trimmed to the relevant part.
const bootstrapSnippet = "return`wss://${L.suffix.toLowerCase()}ws${r}${i}.web.telegram.org/${a}`}"

func TestRewriteBootstrapBundle(t *testing.T) {
	t.Run("legacy mode", func(t *testing.T) {
		out, ok, err := rewriteBootstrapBundle([]byte(bootstrapSnippet), DC{IP: "gateway.example.com", PortBase: 20000})
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "return`wss://gateway.example.com:${20000+r}/${a}`}", string(out))
		require.NotContains(t, string(out), ".web.telegram.org")
	})

	t.Run("domain mode", func(t *testing.T) {
		out, ok, err := rewriteBootstrapBundle([]byte(bootstrapSnippet), DC{Domain: "gateway.example.com"})
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "return`wss://dc${r}.gateway.example.com/${a}`}", string(out))
		require.NotContains(t, string(out), ".web.telegram.org")
	})

	t.Run("custom port base", func(t *testing.T) {
		out, ok, err := rewriteBootstrapBundle([]byte(bootstrapSnippet), DC{IP: "gateway.lan", PortBase: 30000})
		require.NoError(t, err)
		require.True(t, ok)
		require.Contains(t, string(out), "wss://gateway.lan:${30000+r}/${a}")
	})

	t.Run("minifier renames variables", func(t *testing.T) {
		// Only literals and the .suffix property are pinned; variable names
		// (x, q, z, w) are captured, so a minifier rename must not break it.
		in := "return`wss://${x.suffix.toLowerCase()}ws${q}${z}.web.telegram.org/${w}`}"
		out, ok, err := rewriteBootstrapBundle([]byte(in), DC{Domain: "gateway.example.com"})
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "return`wss://dc${q}.gateway.example.com/${w}`}", string(out))
	})

	t.Run("no pattern is fail-safe", func(t *testing.T) {
		in := "var x = `wss://other.example.org/${a}`;"
		out, ok, err := rewriteBootstrapBundle([]byte(in), DC{Domain: "gateway.example.com"})
		require.NoError(t, err)
		require.False(t, ok)
		require.Equal(t, in, string(out))
	})

	t.Run("multiple occurrences", func(t *testing.T) {
		in := bootstrapSnippet + ";" + bootstrapSnippet
		out, ok, err := rewriteBootstrapBundle([]byte(in), DC{Domain: "gateway.example.com"})
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, 2, strings.Count(string(out), "wss://dc${r}.gateway.example.com"))
	})
}

// TestProxyBootstrapBundleRewrite: the worker bundle served through the proxy
// must have its hardcoded DC WebSocket endpoint redirected to the DC bridge,
// so the browser's very first MTProto connection goes through the gateway.
func TestProxyBootstrapBundleRewrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(bootstrapSnippet))
	}))
	defer upstream.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }
	handler.DCRewriteDomain = "gateway.example.com"

	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(t.Context(), id.String(), time.Hour)
	require.NoError(t, err)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/index.worker-C6dbvVmt.js", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "wss://dc${r}.gateway.example.com/${a}")
	require.NotContains(t, body, ".web.telegram.org")
}

// TestProxyBootstrapBundleNoRewrite: without DC rewrite config the bundle
// passes through untouched.
func TestProxyBootstrapBundleNoRewrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(bootstrapSnippet))
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/index.worker-C6dbvVmt.js", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, bootstrapSnippet, readBody(t, resp))
}

func TestIsBundleResponse(t *testing.T) {
	mk := func(ct, ce string, cl int64) *http.Response {
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		if ce != "" {
			h.Set("Content-Encoding", ce)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h, ContentLength: cl}
	}
	require.True(t, isBundleResponse(mk("application/javascript", "", -1), "/index.worker-C6dbvVmt.js"))
	require.True(t, isBundleResponse(mk("text/javascript; charset=utf-8", "", 1<<20), "/index.worker-C6dbvVmt.js"))
	require.False(t, isBundleResponse(mk("application/javascript", "", -1), "/index-ChbE9pGv.js"), "only the worker chunk carries the bootstrap config")
	require.False(t, isBundleResponse(mk("application/javascript", "gzip", -1), "/index.worker-C6dbvVmt.js"), "compressed bodies are not rewritten")
	require.False(t, isBundleResponse(mk("text/html", "", -1), "/index.worker-C6dbvVmt.js"))
	require.False(t, isBundleResponse(mk("application/javascript", "", maxBundleBody+1), "/index.worker-C6dbvVmt.js"), "oversized bodies are not buffered")
}
