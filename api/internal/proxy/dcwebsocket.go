package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/company/telegram-gateway/api/internal/session"
	utls "github.com/refraction-networking/utls"
)

// DC traffic shares the gateway origin and its host-only session cookie.
// Only the fixed Telegram DC map is reachable; this is never an open proxy.
func (h *Handler) serveDC(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, gatewayPrefix+"dc/"), "/")
	if len(parts) != 2 || parts[1] != "apiws" {
		http.NotFound(w, r)
		return
	}
	dc, err := strconv.Atoi(parts[0])
	host, ok := DCHosts[dc]
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket required", http.StatusBadRequest)
		return
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Scheme != "https" || origin.Host != r.Host || origin.User != nil {
		http.Error(w, "invalid websocket origin", http.StatusForbidden)
		return
	}
	sid := cookieValue(r)
	uid, err := h.Sessions.Get(r.Context(), sid)
	if err != nil {
		unauthorized(w, r)
		return
	}
	h.dcMu.Lock()
	if h.dcActive == nil {
		h.dcActive = make(map[string]int)
	}
	if h.dcTotal >= 256 || h.dcActive[uid] >= 16 {
		h.dcMu.Unlock()
		http.Error(w, "too many DC connections", http.StatusTooManyRequests)
		return
	}
	h.dcTotal++
	h.dcActive[uid]++
	h.dcMu.Unlock()
	defer func() {
		h.dcMu.Lock()
		defer h.dcMu.Unlock()
		h.dcTotal--
		h.dcActive[uid]--
		if h.dcActive[uid] == 0 {
			delete(h.dcActive, uid)
		}
	}()

	transport := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 15 * time.Second,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, dcProxyDialTimeout)
			defer cancel()
			conn, err := h.dcDial(ctx, network, net.JoinHostPort(host, "443"))
			if err != nil {
				return nil, err
			}
			handshake := h.dcHandshake
			if handshake == nil {
				handshake = func(ctx context.Context, c net.Conn, host string) (net.Conn, error) {
					return chromeTLS(ctx, c, host, false)
				}
			}
			tlsConn, err := handshake(ctx, conn, host)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			guarded := &revocableConn{Conn: tlsConn, done: make(chan struct{})}
			go h.watchDC(guarded, sid)
			return guarded, nil
		},
	}
	defer transport.CloseIdleConnections()
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = host
			req.URL.Path = "/apiws"
			req.URL.RawPath = ""
			req.URL.RawQuery = ""
			req.Host = host
			// Gateway secrets and client identity must never leave the gateway.
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			req.Header.Del("X-CSRF-Token")
			req.Header.Del("Forwarded")
			req.Header.Del("X-Real-IP")
			req.Header.Del("X-Forwarded-Proto")
			req.Header["X-Forwarded-For"] = nil
			req.Header.Set("Origin", "https://web.telegram.org")
		},
		Transport:      transport,
		ModifyResponse: func(resp *http.Response) error { resp.Header.Del("Set-Cookie"); return nil },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			dcFailures.Inc()
			http.Error(w, "Telegram DC unavailable", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

type revocableConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *revocableConn) Close() error {
	var err error
	c.once.Do(func() { close(c.done); err = c.Conn.Close() })
	return err
}
func (h *Handler) watchDC(conn *revocableConn, sid string) {
	interval := h.dcCheckInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-conn.done:
			return
		case <-timer.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			valid := h.dcAuthorized(ctx, sid)
			cancel()
			if !valid {
				_ = conn.Close()
				return
			}
		}
	}
}
func (h *Handler) dcAuthorized(ctx context.Context, sid string) bool {
	uid, err := h.Sessions.Get(ctx, sid)
	if err != nil {
		return false
	}
	id, err := parseUUID(uid)
	if err != nil {
		return false
	}
	u, err := h.Queries.GetUserByID(ctx, id)
	if err != nil || !u.IsActive || u.BlockedAt.Valid || (u.ExpiresAt.Valid && !u.ExpiresAt.Time.After(time.Now())) {
		return false
	}

	if versioned, ok := h.Sessions.(session.VersionedStore); ok {
		revision, err := versioned.Revision(ctx, sid)
		if err != nil || revision != session.CredentialRevision(u.PasswordHash.String, u.PasswordChangedAt.Time) {
			return false
		}
	}
	ok, err := h.Queries.HasUserConsent(ctx, id)
	return err == nil && ok
}

// Chrome-compatible handshake is required by Telegram's WebSocket edge.
func chromeTLS(ctx context.Context, conn net.Conn, host string, insecure bool) (net.Conn, error) {
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_120)
	if err != nil {
		return nil, err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	tc := utls.UClient(conn, &utls.Config{ServerName: host, InsecureSkipVerify: insecure}, utls.HelloCustom)
	if err := tc.ApplyPreset(&spec); err != nil {
		return nil, fmt.Errorf("TLS preset: %w", err)
	}
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return tc, nil
}
