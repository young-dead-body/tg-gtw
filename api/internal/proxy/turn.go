package proxy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/company/telegram-gateway/api/internal/turnrelay"
)

// TURN hands Telegram Web the gateway's TURN relay for call media (see
// package turnrelay). The proxy injects rtc.js into the app's HTML page; the
// script wraps RTCPeerConnection and fetches short-lived credentials from
// gatewayTURNPath for the logged-in user.
type TURN struct {
	// URLs are the ICE server URLs, e.g.
	// "turns:turn.web-tgk.example.ru:443?transport=tcp".
	URLs []string
	// Secret is the shared secret of the relay's TURN REST credentials.
	Secret string
	// TTL is how long issued credentials stay valid.
	TTL time.Duration
	// RelayOnly makes the browser use relayed candidates only.
	RelayOnly bool
}

const (
	gatewayPrefix    = "/tg/_gw/"
	gatewayScript    = gatewayPrefix + "rtc.js"
	gatewayTURNPath  = gatewayPrefix + "turn"
	maxInjectHTMLLen = 4 << 20
)

//go:embed rtc.js
var rtcScript []byte

var rtcScriptTag = []byte(`<script src="` + gatewayScript + `"></script>`)

// serveGateway answers the gateway's own /tg/_gw/* endpoints; it reports
// false for every other path, which is then proxied to Telegram.
func (h *Handler) serveGateway(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, gatewayPrefix) {
		return false
	}
	if strings.HasPrefix(r.URL.Path, gatewayPrefix+"dc/") {
		if !h.SecureDC {
			http.NotFound(w, r)
		} else {
			h.serveDC(w, r)
		}
		return true
	}
	if r.URL.Path == gatewayPrefix+"status" {
		available := false
		if h.Pool != nil {
			backends, err := h.Pool.Active(r.Context())
			available = err == nil && len(backends) > 0
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]bool{"available": available})
		return true
	}
	if h.TURN == nil {
		http.NotFound(w, r)
		return true
	}
	switch r.URL.Path {
	case gatewayScript:
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(rtcScript)
	case gatewayTURNPath:
		h.serveTURNCredentials(w, r)
	default:
		http.NotFound(w, r)
	}
	return true
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type turnCredentials struct {
	IceServers []iceServer `json:"iceServers"`
	TTL        int64       `json:"ttl"`
	RelayOnly  bool        `json:"relayOnly"`
}

func (h *Handler) serveTURNCredentials(w http.ResponseWriter, r *http.Request) {
	// The gate already validated the session; its user id labels the
	// credentials so relay logs can be tied to an account.
	user, err := h.Sessions.Get(r.Context(), cookieValue(r))
	if err != nil {
		unauthorized(w, r)
		return
	}
	ttl := h.TURN.TTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	username, password, err := turnrelay.Credentials(h.TURN.Secret, user, ttl)
	if err != nil {
		http.Error(w, "turn credentials unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(turnCredentials{
		IceServers: []iceServer{{URLs: h.TURN.URLs, Username: username, Credential: password}},
		TTL:        int64(ttl / time.Second),
		RelayOnly:  h.TURN.RelayOnly,
	})
}

// injectRTCScript loads rtc.js ahead of the app bundle in Telegram Web's HTML
// pages. Compressed bodies are left alone (nginx forces identity upstream).
func (h *Handler) injectRTCScript(resp *http.Response) error {
	if h.TURN == nil || resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && ce != "identity" {
		log.Printf("proxy: rtc.js not injected: HTML body is %s-encoded", ce)
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInjectHTMLLen+1))
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read html: %w", err)
	}
	if len(body) <= maxInjectHTMLLen {
		body = insertAfterHead(body, rtcScriptTag)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

// insertAfterHead places tag right after the opening <head> element so it
// runs before any of the page's own scripts; without <head> it is prepended.
func insertAfterHead(html, tag []byte) []byte {
	lower := bytes.ToLower(html)
	i := bytes.Index(lower, []byte("<head"))
	for i >= 0 {
		// Skip <header> and similar tags that merely start with "<head".
		next := i + len("<head")
		if next < len(lower) && (lower[next] == '>' || lower[next] == ' ' || lower[next] == '\t' || lower[next] == '\n' || lower[next] == '\r') {
			end := bytes.IndexByte(lower[next:], '>')
			if end < 0 {
				break
			}
			pos := next + end + 1
			out := make([]byte, 0, len(html)+len(tag))
			out = append(out, html[:pos]...)
			out = append(out, tag...)
			return append(out, html[pos:]...)
		}
		j := bytes.Index(lower[next:], []byte("<head"))
		if j < 0 {
			break
		}
		i = next + j
	}
	return append(append([]byte(nil), tag...), html...)
}
