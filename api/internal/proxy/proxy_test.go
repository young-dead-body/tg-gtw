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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/session"
)

// fakeQuerier implements the Querier subsets needed by Handler and Pool.
type fakeQuerier struct {
	mu        sync.Mutex
	users     map[string]db.User
	consented map[string]bool
	proxies   []db.ProxyPool
	marked    []db.UpdateProxyHealthParams
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		users:     map[string]db.User{},
		consented: map[string]bool{},
	}
}

func (f *fakeQuerier) ListActiveProxies(ctx context.Context) ([]db.ProxyPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proxies, nil
}

func (f *fakeQuerier) ListProxies(ctx context.Context) ([]db.ProxyPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proxies, nil
}

func (f *fakeQuerier) UpdateProxyHealth(ctx context.Context, arg db.UpdateProxyHealthParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, arg)
	return nil
}

func (f *fakeQuerier) GetUserByID(ctx context.Context, id pgtype.UUID) (db.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id.String()]
	if !ok {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeQuerier) HasUserConsent(ctx context.Context, userID pgtype.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consented[userID.String()], nil
}

func (f *fakeQuerier) addUser(id pgtype.UUID, u db.User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id.String()] = u
}

// fakeStore is an in-memory session.Store.
type fakeStore struct {
	mu     sync.Mutex
	byID   map[string]string
	byUser map[string][]string
	seq    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: map[string]string{}, byUser: map[string][]string{}}
}

func (s *fakeStore) Create(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("sess-%d", s.seq)
	s.byID[id] = userID
	s.byUser[userID] = append(s.byUser[userID], id)
	return id, nil
}

func (s *fakeStore) Get(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, ok := s.byID[id]
	if !ok {
		return "", session.ErrNotFound
	}
	return userID, nil
}

func (s *fakeStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
	return nil
}

func (s *fakeStore) DeleteUser(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.byUser[userID] {
		delete(s.byID, id)
	}
	delete(s.byUser, userID)
	return nil
}

func uuidFromBytes(b byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{b}, Valid: true}
}

func testUser(id pgtype.UUID) db.User {
	return db.User{
		ID:        id,
		FirstName: "Alice",
		Role:      "user",
		IsActive:  true,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
}

// gatedFixture builds a handler whose gate is satisfied (valid session + consent).
func gatedFixture(t *testing.T, upstreamURL string) (*Handler, string) {
	t.Helper()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstreamURL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }

	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)
	return handler, sid
}

func gatedRequest(handler *Handler, sid, method, target string, body io.Reader) *http.Response {
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func readBodyBytes(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

// newSession creates a session cookie for the fixture user.
func newSession(t *testing.T, handler *Handler) string {
	t.Helper()
	id := uuidFromBytes(9)
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)
	return sid
}

// gatedHandler builds a handler with DC rewrite enabled.
func gatedHandler(t *testing.T, upstreamURL, ip string, portBase int) *Handler {
	t.Helper()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstreamURL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }
	handler.DCRewriteIP = ip
	handler.DCPortBase = portBase
	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	return handler
}

func TestGateNoCookie(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("gate must reject before reaching the backend")
	}))
	defer upstream.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

	req := httptest.NewRequest(http.MethodGet, "/tg/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestGateAnonymousAssets: manifest, service worker and favicon are fetched by
// the browser without session credentials, so they must pass the gate.
func TestGateAnonymousAssets(t *testing.T) {
	for _, path := range []string{
		"/tg/site.webmanifest",
		"/tg/site_apple.webmanifest?v=abc",
		"/tg/manifest.json",
		"/tg/sw.js",
		"/tg/sw-FkKtoDCn.js",
		"/tg/favicon.ico",
		"/tg/favicon-32x32.png",
	} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			defer upstream.Close()
			q := newFakeQuerier()
			q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
			handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestGateAnonymousAssetExactMatch(t *testing.T) {
	// Applies only to the exact static filenames, not e.g. API paths that
	// happen to start with "sw" or "manifest".
	for _, test := range []struct {
		path string
		ok   bool
	}{
		{"/tg/sw-crypto.js", true},
		{"/tg/swagger", false},
		{"/tg/swsomething", false},
		{"/tg/manifest-extra", false},
		{"/tg/site.webmanifest.meta", false},
	} {
		require.Equal(t, test.ok, isAnonymousAsset(test.path), test.path)
	}
}

// flakyBackend answers 502 fails times, then 200. It counts every request.
type flakyBackend struct {
	fails    int
	mu       sync.Mutex
	requests int
}

func (f *flakyBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	f.mu.Lock()
	fail := f.requests <= f.fails
	f.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *flakyBackend) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func gateTestHandler(q *fakeQuerier, backend *httptest.Server) *Handler {
	h := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	h.ConfigureRetry(2, time.Millisecond)
	return h
}

func TestRetryTransientBackendFailure(t *testing.T) {
	backend := &flakyBackend{fails: 2}
	srv := httptest.NewServer(backend)
	defer srv.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: srv.URL, Status: "active"}}
	handler := gateTestHandler(q, srv)

	req := httptest.NewRequest(http.MethodGet, "/tg/sw.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 3, backend.count(), "two 502s must be retried, third attempt succeeds")
}

func TestRetryExhaustedOnPersistentFailure(t *testing.T) {
	backend := &flakyBackend{fails: 100}
	srv := httptest.NewServer(backend)
	defer srv.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: srv.URL, Status: "active"}}
	handler := gateTestHandler(q, srv)

	req := httptest.NewRequest(http.MethodGet, "/tg/sw.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, 3, backend.count(), "retries exhausted after retries+1 attempts")
}

func TestRetrySkipsNonIdempotentMethods(t *testing.T) {
	t.Run("POST", func(t *testing.T) {
		backend := &flakyBackend{fails: 100}
		srv := httptest.NewServer(backend)
		defer srv.Close()
		q := newFakeQuerier()
		q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: srv.URL, Status: "active"}}
		handler := gateTestHandler(q, srv)

		req := httptest.NewRequest(http.MethodPost, "/tg/sw.js", strings.NewReader("x"))
		req.Header.Set("Content-Type", "application/octet-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.Equal(t, 1, backend.count(), "non-idempotent requests must not be retried")
	})
}

func TestGateInvalidSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

	req := httptest.NewRequest(http.MethodGet, "/tg/x", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "does-not-exist"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestGateRedirectsNavigationToLogin: a browser page load without a valid
// session goes to the login page; non-navigation requests keep a plain 401.
func TestGateRedirectsNavigationToLogin(t *testing.T) {
	for _, test := range []struct {
		name     string
		method   string
		cookie   string
		headers  map[string]string
		redirect bool
	}{
		{"no cookie, Accept html", http.MethodGet, "", map[string]string{"Accept": "text/html,application/xhtml+xml"}, true},
		{"no cookie, Sec-Fetch-Mode navigate", http.MethodGet, "", map[string]string{"Sec-Fetch-Mode": "navigate"}, true},
		{"invalid session, Accept html", http.MethodGet, "does-not-exist", map[string]string{"Accept": "text/html"}, true},
		{"fetch with Accept html", http.MethodGet, "", map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "cors"}, false},
		{"websocket upgrade", http.MethodGet, "", map[string]string{"Accept": "text/html", "Connection": "Upgrade", "Upgrade": "websocket"}, false},
		{"POST", http.MethodPost, "", map[string]string{"Accept": "text/html"}, false},
		{"XHR", http.MethodGet, "", map[string]string{"Accept": "application/json"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("gate must reject before reaching the backend")
			}))
			defer upstream.Close()
			q := newFakeQuerier()
			q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
			handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

			req := httptest.NewRequest(test.method, "/tg/", nil)
			if test.cookie != "" {
				req.AddCookie(&http.Cookie{Name: session.CookieName, Value: test.cookie})
			}
			for k, v := range test.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if test.redirect {
				require.Equal(t, http.StatusFound, rec.Code)
				require.Equal(t, "/", rec.Header().Get("Location"))
			} else {
				require.Equal(t, http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

func TestGateBlockedUser(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

	id := uuidFromBytes(9)
	u := testUser(id)
	u.IsActive = false
	q.addUser(id, u)
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/tg/x", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "blocked")
}

func TestGateMissingConsent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})

	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/tg/x", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "consent")
}

func TestProxyHTTP(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/some/page/123", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "upstream-ok", readBody(t, resp))
	require.Equal(t, "/some/page/123", gotPath)
}

// TestProxyServiceWorkerRewrite: the app registers its service worker under a
// version-hashed name (sw-<hash>.js) and the backend serves that exact file;
// the canonical sw.js is the legacy webogram worker without the /stream and
// /download fetch routing, so hashed SW names must pass through unchanged.
func TestProxyServiceWorkerRewrite(t *testing.T) {
	for _, test := range []struct {
		urlPath string // request path under /tg
		want    string // upstream path (base /k prepended by the pool URL)
	}{
		{"/tg/sw.js", "/k/sw.js"},
		{"/tg/sw-FkKtoDCn.js", "/k/sw-FkKtoDCn.js"},
		{"/tg/sw-CF4_-4IQ.js", "/k/sw-CF4_-4IQ.js"},
		{"/tg/sw.js?v=hashed", "/k/sw.js"},
		{"/tg/site.webmanifest", "/k/site.webmanifest"},
	} {
		t.Run(test.urlPath, func(t *testing.T) {
			var gotPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				_, _ = w.Write([]byte("sw-ok"))
			}))
			defer upstream.Close()

			handler, sid := gatedFixture(t, upstream.URL+"/k")
			resp := gatedRequest(handler, sid, http.MethodGet, test.urlPath, nil)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, test.want, gotPath)
		})
	}
}

func TestProxyDownload(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 1024*1024) // 16 MiB
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/media/file.bin", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestProxyUpload(t *testing.T) {
	body := bytes.Repeat([]byte("upload-payload-"), 1024*1024)
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_, _ = w.Write([]byte("saved"))
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	req := httptest.NewRequest(http.MethodPost, "/tg/upload/file.bin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, body, got)
}

func TestProxyHTMLNotCached(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/page"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=3600")
			_, _ = w.Write([]byte("<html>web</html>"))
		default:
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Cache-Control", "max-age=31536000")
			_, _ = w.Write([]byte("app.js"))
		}
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/page", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-cache, no-store, must-revalidate", resp.Header.Get("Cache-Control"))
	require.Equal(t, "no-cache", resp.Header.Get("Pragma"))

	resp = gatedRequest(handler, sid, http.MethodGet, "/tg/app.js", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "max-age=31536000", resp.Header.Get("Cache-Control"))
	require.Empty(t, resp.Header.Get("Pragma"))
}

func TestProxyPreservesBackendBasePath(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL + "/a", Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/some/page", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/a/some/page", gotPath)
}

func TestAutoSwitchToHealthyBackend(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var upstreamCalls int
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		_, _ = w.Write([]byte("healthy"))
	}))
	defer healthy.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{
		{ID: uuidFromBytes(1), Url: deadURL, Status: "active"},
		{ID: uuidFromBytes(2), Url: healthy.URL, Status: "active"},
	}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }
	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/ok", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "healthy", readBody(t, resp))
	require.Equal(t, 1, upstreamCalls)

	q.mu.Lock()
	defer q.mu.Unlock()
	require.Len(t, q.marked, 1)
	require.Equal(t, "inactive", q.marked[0].Status)
}

func TestAllBackendsDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: deadURL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q})
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }
	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/ok", nil)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "all proxy backends failed")
}

func TestPoolHealthCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{
		{ID: uuidFromBytes(1), Url: ok.URL, Status: "active"},
		{ID: uuidFromBytes(2), Url: deadURL, Status: "active"},
	}
	pool := &Pool{Queries: q, CheckTimeout: 500 * time.Millisecond}

	require.True(t, pool.Healthy(q.proxies[0]))
	require.False(t, pool.Healthy(q.proxies[1]))

	pool.CheckOnce(context.Background())

	q.mu.Lock()
	defer q.mu.Unlock()
	require.Len(t, q.marked, 1)
	require.Equal(t, uuidFromBytes(2).String(), q.marked[0].ID.String())
	require.Equal(t, "inactive", q.marked[0].Status)
}

func TestPoolRevivesInactiveBackend(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()

	q := newFakeQuerier()
	// Backend is listed as inactive (was demoted), but the host is back up.
	q.proxies = []db.ProxyPool{
		{ID: uuidFromBytes(7), Url: ok.URL, Status: "inactive"},
	}
	pool := &Pool{Queries: q, CheckTimeout: 500 * time.Millisecond}

	pool.CheckOnce(context.Background())

	q.mu.Lock()
	defer q.mu.Unlock()
	require.Len(t, q.marked, 1)
	require.Equal(t, uuidFromBytes(7).String(), q.marked[0].ID.String())
	require.Equal(t, "active", q.marked[0].Status)
}

// ---------------------------------------------------------------------------
// WebSocket pass-through via a raw TCP upgrade.
// ---------------------------------------------------------------------------

func TestProxyWebSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("expected websocket upgrade, got %q", r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("backend does not support hijack")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n"
		if _, err := bufrw.WriteString(resp); err != nil {
			return
		}
		if err := bufrw.Flush(); err != nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := bufrw.Read(buf)
			if n > 0 {
				_, _ = bufrw.Write(buf[:n])
				_ = bufrw.Flush()
			}
			if err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = fmt.Fprintf(conn, "GET /tg/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nCookie: %s=%s\r\n\r\n",
		addr, session.CookieName, sid)
	require.NoError(t, err)

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, status, "101")

	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" {
			break
		}
	}

	msg := []byte("ping-over-ws")
	_, err = conn.Write(msg)
	require.NoError(t, err)

	echo := make([]byte, len(msg))
	_, err = io.ReadFull(br, echo)
	require.NoError(t, err)
	require.Equal(t, msg, echo)
}

func TestProxyRewritesConfigDCOptions(t *testing.T) {
	inner := buildConfigTL(encodeCount(
		buildDCOption(1, 0, "149.154.175.50", 443, nil),
		buildDCOption(2, 0, "149.154.167.51", 443, nil),
	))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(inner)
	}))
	defer upstream.Close()

	handler := gatedHandler(t, upstream.URL, "192.0.2.10", 20000)
	sid := newSession(t, handler)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/api/config", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))

	opts := parseDCOptions(t, readBodyBytes(t, resp))
	require.Len(t, opts, 2)
	require.Equal(t, "192.0.2.10", opts[0].IP)
	require.Equal(t, int32(20001), opts[0].Port)
	require.Equal(t, "192.0.2.10", opts[1].IP)
	require.Equal(t, int32(20002), opts[1].Port)
}

func TestProxyRewritesConfigDCDomainMode(t *testing.T) {
	inner := buildConfigTL(encodeCount(
		buildDCOption(1, 0, "149.154.175.50", 443, nil),
		buildDCOption(2, 0, "149.154.167.51", 443, nil),
	))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(inner)
	}))
	defer upstream.Close()

	handler := gatedHandler(t, upstream.URL, "", 0)
	handler.DCRewriteDomain = "gateway.example.com"
	sid := newSession(t, handler)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/api/config", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	opts := parseDCOptions(t, readBodyBytes(t, resp))
	require.Len(t, opts, 2)
	require.Equal(t, "dc1.gateway.example.com", opts[0].IP)
	require.Equal(t, int32(443), opts[0].Port)
	require.Equal(t, "dc2.gateway.example.com", opts[1].IP)
	require.Equal(t, int32(443), opts[1].Port)
}

func TestProxyConfigDCRewriteDisabledByDefault(t *testing.T) {
	inner := buildConfigTL(encodeCount(buildDCOption(1, 0, "149.154.175.50", 443, nil)))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(inner)
	}))
	defer upstream.Close()

	handler, sid := gatedFixture(t, upstream.URL)
	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/config", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	opts := parseDCOptions(t, readBodyBytes(t, resp))
	require.Equal(t, "149.154.175.50", opts[0].IP)
	require.Equal(t, int32(443), opts[0].Port)
}

func TestProxyConfigPassesThroughNonConfig(t *testing.T) {
	data := []byte("not-a-tl-config at all, just a plain response body")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer upstream.Close()

	handler := gatedHandler(t, upstream.URL, "192.0.2.10", 20000)
	sid := newSession(t, handler)
	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/other", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, string(data), readBody(t, resp))
}
