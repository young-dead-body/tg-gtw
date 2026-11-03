package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/session"
)

// Querier is the subset of db.Querier the proxy needs.
type Querier interface {
	GetUserByID(ctx context.Context, id pgtype.UUID) (db.User, error)
	HasUserConsent(ctx context.Context, userID pgtype.UUID) (bool, error)
}

// Handler is the Telegram Web reverse proxy. It gates every request behind a
// valid session and an accepted consent, then forwards it to the first healthy
// backend from the proxy pool, failing over automatically when a backend is
// unreachable. ReverseProxy handles WebSocket upgrades and response streaming.
type Handler struct {
	Queries  Querier
	Sessions session.Store
	Pool     *Pool
	// TargetRewrite optionally rewrites the proxied path; nil strips the /tg
	// prefix so the backend receives the original Telegram path.
	TargetRewrite func(path string) string

	// DCRewriteIP and DCPortBase, when set, rewrite the dc_options block of
	// every Telegram Web config#330b4067 response passing through the proxy:
	// ip_address becomes DCRewriteIP and the port becomes DCPortBase+dc_id,
	// pointing the browser's MTProto connections at the gateway's DC bridge
	// instead of the (blocked) real Telegram datacenters. This is the legacy
	// LAN mode: every DC lands on the same address on a DC-specific port.
	DCRewriteIP string
	DCPortBase  int

	// DCRewriteDomain, when set, switches to the domain mode: every DC gets
	// its own hostname dc<N>.<Domain> on port 443, and the public entrypoint
	// routes the connection by TLS SNI to the matching DC bridge. This is the
	// only mode that works when clients cannot reach non-standard ports
	// (e.g. corporate networks in RU allow only 80/443).
	DCRewriteDomain string

	// TURN, when set, serves the gateway TURN relay credentials under
	// /tg/_gw/ and injects rtc.js into Telegram Web pages (see turn.go).
	TURN *TURN

	SecureDC        bool
	dcDial          DialContext
	dcHandshake     func(context.Context, net.Conn, string) (net.Conn, error)
	dcCheckInterval time.Duration
	dcMu            sync.Mutex
	dcActive        map[string]int
	dcTotal         int
	transport       http.RoundTripper
}

// Default values for the upstream retry policy.
const (
	// DefaultRetries is the number of extra attempts for GET/HEAD requests
	// that fail before the response body starts (or come back 502/503).
	DefaultRetries = 2
	// DefaultRetryDelay is the pause between attempts.
	DefaultRetryDelay = 200 * time.Millisecond
)

// NewHandler wires the proxy with a transport tuned for long-lived streams
// (WebSocket, media download) and large uploads. An optional DialContext
// (e.g. SOCKS5 from NewDialContext) tunnels every upstream connection; when
// omitted the transport dials directly. Upstream GET/HEAD requests are
// retried DefaultRetries times with DefaultRetryDelay between attempts.
func NewHandler(q Querier, store session.Store, pool *Pool, dial ...DialContext) *Handler {
	dc := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	if len(dial) > 0 && dial[0] != nil {
		dc = dial[0]
	}
	base := &http.Transport{
		DialContext:           dc,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &Handler{
		Queries:  q,
		dcDial:   dc,
		Sessions: store,
		Pool:     pool,
		transport: &retryTransport{
			base:    base,
			retries: DefaultRetries,
			delay:   DefaultRetryDelay,
		},
	}
}

// ConfigureRetry overrides the upstream retry policy (attempts and delay).
func (h *Handler) ConfigureRetry(retries int, delay time.Duration) {
	rt, ok := h.transport.(*retryTransport)
	if !ok {
		return
	}
	if retries < 0 {
		retries = 0
	}
	rt.retries = retries
	if delay < 0 {
		delay = 0
	}
	rt.delay = delay
}

// ServeHTTP is the entry point for /tg/* traffic.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.gate(w, r) {
		return
	}
	if h.serveGateway(w, r) {
		return
	}
	h.proxy(w, r)
}

// gate enforces "активная сессия + принятое согласие" (ТЗ §10).
func (h *Handler) gate(w http.ResponseWriter, r *http.Request) bool {
	if isAnonymousAsset(r.URL.Path) {
		return true
	}

	sid := cookieValue(r)
	if sid == "" {
		unauthorized(w, r)
		return false
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	userID, err := h.Sessions.Get(ctx, sid)
	if err != nil {
		unauthorized(w, r)
		return false
	}

	id, err := parseUUID(userID)
	if err != nil {
		unauthorized(w, r)
		return false
	}

	u, err := h.Queries.GetUserByID(ctx, id)
	if err != nil {
		unauthorized(w, r)
		return false
	}
	if versioned, ok := h.Sessions.(session.VersionedStore); ok {
		revision, err := versioned.Revision(ctx, sid)
		if err != nil || revision != session.CredentialRevision(u.PasswordHash.String, u.PasswordChangedAt.Time) {
			unauthorized(w, r)
			return false
		}
	}

	if !u.IsActive || u.BlockedAt.Valid {
		http.Error(w, "forbidden: account disabled or blocked", http.StatusForbidden)
		return false
	}
	if u.ExpiresAt.Valid && u.ExpiresAt.Time.Before(time.Now()) {
		http.Error(w, "forbidden: access expired", http.StatusForbidden)
		return false
	}

	ok, err := h.Queries.HasUserConsent(ctx, id)
	if err != nil || !ok {
		http.Error(w, "consent required", http.StatusForbidden)
		return false
	}

	return true
}

// unauthorized sends a browser navigation without a valid session to the login
// page ("/"); XHR, WebSocket and asset requests keep getting a plain 401, since
// a redirect to an HTML page would only break the Telegram Web client.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	if isNavigation(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// isNavigation reports whether r is a top-level page load by the browser.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// proxy forwards the request to the first healthy backend, switching on failure.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	backends, err := h.backends(r.Context())
	if err != nil {
		http.Error(w, "proxy pool unavailable", http.StatusServiceUnavailable)
		return
	}

	for _, backend := range backends {
		if !h.Pool.Healthy(backend) {
			h.Pool.Mark(r.Context(), backend.ID.String(), "inactive")
			continue
		}
		if h.forward(w, r, backend) {
			return
		}
		h.Pool.Mark(r.Context(), backend.ID.String(), "inactive")
	}

	http.Error(w, "all proxy backends failed", http.StatusBadGateway)
}

func (h *Handler) backends(ctx context.Context) ([]db.ProxyPool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return h.Pool.Active(ctx)
}

// forward proxies a single request to one backend. It returns true when the
// upstream produced a complete response (including legitimate 4xx/5xx); it
// returns false only when the backend was unreachable, so the caller switches.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, backend db.ProxyPool) bool {
	target, err := url.Parse(backend.Url)
	if err != nil {
		return false
	}

	failed := false
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			h.rewriteTarget(req)
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Preserve the backend's base path (e.g. /a from
			// https://web.telegram.org/a): with the /tg prefix stripped the
			// request would otherwise land on the backend root, serving a
			// different app than configured.
			if base := strings.TrimSuffix(target.Path, "/"); base != "" {
				req.URL.Path = base + req.URL.Path
			}
			req.URL.RawPath = ""
			req.Host = target.Host
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			req.Header.Del("X-CSRF-Token")
			if h.SecureDC {
				req.Header.Del("If-None-Match")
				req.Header.Del("If-Modified-Since")
				req.Header.Set("Accept-Encoding", "identity")
			}
			if req.Header.Get("X-Forwarded-For") == "" {
				req.Header.Set("X-Forwarded-For", r.RemoteAddr)
			}
			req.Header.Set("X-Forwarded-Proto", "https")
		},
		Transport:     h.transport,
		FlushInterval: -1, // stream media/download immediately
		// HTML pages from Telegram carry cache-control: max-age=3600. When the
		// proxied client is replaced (e.g. webogram -> Web A) browsers would
		// keep serving the stale page from cache, so HTML is never cached.
		// HTML pages from Telegram carry cache-control: max-age=3600. When the
		// proxied client is replaced (e.g. webogram -> Web A) browsers would
		// keep serving the stale page from cache, so HTML is never cached.
		ModifyResponse: func(resp *http.Response) error {
			if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
				resp.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
				resp.Header.Set("Pragma", "no-cache")
			}
			if err := h.injectRTCScript(resp); err != nil {
				return err
			}
			rw := h.dcRewrite()
			if rw.Enabled() && !rw.SameOrigin && isTLPayload(resp) {
				body, ok, n, err := rewriteConfigBody(resp.Body, rw)
				if err != nil {
					return err
				}
				// rewriteConfigBody always consumes and closes the original
				// stream (it had to buffer the payload for TL parsing), so the
				// re-wrapped body must be attached even when no dc_options
				// were rewritten — otherwise the closed reader would be served
				// and every response would die with "read on closed body".
				resp.Body = body
				RewriteResult("config", ok)
				if ok {
					resp.Header.Del("ETag")
					resp.Header.Del("Last-Modified")
					if h.SecureDC {
						resp.Header.Set("Cache-Control", "no-store")
					}
					resp.ContentLength = n
					resp.Header.Set("Content-Length", strconv.FormatInt(n, 10))
				}
			}
			// Bootstrap bundle rewrite: the worker's hardcoded DC WebSocket
			// endpoints must land on the DC bridge before the first getConfig
			// response exists (see bundle_rewrite.go). Like rewriteConfigBody,
			// the original stream is always consumed, so the re-wrapped body is
			// attached unconditionally.

			if h.SecureDC && strings.Contains(r.URL.Path, "index.worker-") && strings.HasSuffix(r.URL.Path, ".js") && resp.StatusCode == http.StatusOK && !isBundleResponse(resp, r.URL.Path) {
				RewriteResult("bundle", false)
				return fmt.Errorf("unsupported encoded or oversized Telegram worker")
			}
			if rw.Enabled() && isBundleResponse(resp, r.URL.Path) {
				body, ok, n, err := rewriteBundleBody(resp.Body, rw)
				if err != nil {
					return err
				}
				resp.Body = body
				if ok {
					resp.Header.Del("ETag")
					resp.Header.Del("Last-Modified")
					if h.SecureDC {
						resp.Header.Set("Cache-Control", "no-store")
					}
					resp.ContentLength = n
					resp.Header.Set("Content-Length", strconv.FormatInt(n, 10))
					if rw.SameOrigin {
						log.Printf("proxy: bootstrap bundle rewritten to authenticated same-origin DC")
					} else if rw.Domain != "" {
						log.Printf("proxy: bootstrap bundle rewritten -> wss://dc<N>.%s (DC bridge, SNI)", rw.Domain)
					} else {
						log.Printf("proxy: bootstrap bundle rewritten -> wss://%s:%d+dc (DC bridge)", rw.IP, rw.PortBase)
					}
				}
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			failed = true
			log.Printf("proxy: backend %s failed: %v", backend.Url, err)
		},
	}

	rp.ServeHTTP(w, r)
	return !failed
}

// rewriteTarget transforms the incoming request for the backend. The /tg
// prefix is stripped by default so Telegram paths are preserved.
func (h *Handler) rewriteTarget(req *http.Request) {
	p := req.URL.Path
	if h.TargetRewrite != nil {
		p = h.TargetRewrite(p)
	} else {
		p = strings.TrimPrefix(p, "/tg")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
	}
	// The app registers its service worker under a version-hashed name
	// (sw-<hash>.js, e.g. sw-CF4_-4IQ.js) and the backend serves that exact
	// file: the canonical sw.js on web.telegram.org/k is the legacy webogram
	// worker without the /stream, /download, /hls fetch routing, so any
	// sw-*.js must pass through unchanged or the modern SW never activates
	// and video streaming breaks. The backend's base path is prepended by
	// the Director.
	req.URL.Path = p
	req.URL.RawPath = ""
}

func parseUUID(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

// dcRewrite resolves the active DC endpoint rewrite configuration: the domain
// mode (per-DC hostnames on port 443) wins over the legacy IP mode.
func (h *Handler) dcRewrite() DC {
	if h.SecureDC {
		return DC{SameOrigin: true}
	}
	if h.DCRewriteDomain != "" {
		return DC{Domain: h.DCRewriteDomain}
	}
	return DC{IP: h.DCRewriteIP, PortBase: h.DCPortBase}
}

// retryTransport retries idempotent (GET/HEAD) upstream requests that fail
// before the response body arrives: connection errors and transient 502/503
// answers (the SOCKS5 egress to web.telegram.org drops connection bursts for
// a second or two). POST and other non-idempotent requests, and streams that
// die mid-body, are passed through untouched — those cannot be retried
// safely. When the retries are exhausted, the last 502/503 response is
// returned as-is (the browser sees the upstream error, not a proxy one).
type retryTransport struct {
	base    http.RoundTripper
	retries int
	delay   time.Duration
}

// drainLimit caps how much of a discarded upstream body is read so the
// underlying connection stays reusable.
const drainLimit = 64 << 10

func (rt *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return rt.base.RoundTrip(req)
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := rt.base.RoundTrip(req)
		switch {
		case err == nil && resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusServiceUnavailable:
			return resp, nil
		case err == nil && attempt == rt.retries:
			return resp, nil
		case err == nil:
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("backend answered %d", resp.StatusCode)
		default:
			lastErr = err
		}

		if attempt == rt.retries {
			return nil, lastErr
		}
		select {
		case <-time.After(rt.delay):
		case <-req.Context().Done():
			return nil, lastErr
		}
	}
}

// isAnonymousAsset reports whether the Path is a Telegram Web static resource
// the browser fetches without session credentials (web app manifest, service
// worker script, favicon). These requests must not fall under the session gate
// or they would always fail with 401. They carry no per-user data, so
// serving them anonymously is safe.
func isAnonymousAsset(path string) bool {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}

	switch {
	case strings.HasSuffix(base, ".webmanifest"):
		return true
	case base == "manifest.json":
		return true
	case base == "sw.js" || strings.HasPrefix(base, "sw-") && strings.HasSuffix(base, ".js"):
		return true
	case base == "favicon.ico" || strings.HasPrefix(base, "favicon-"):
		return true
	}
	return false
}

func cookieValue(r *http.Request) string {
	c, err := r.Cookie(session.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
