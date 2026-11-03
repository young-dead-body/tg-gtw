package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/company/telegram-gateway/api/internal/db"
)

// ---------------------------------------------------------------------------
// Minimal SOCKS5 (RFC 1928) server for tests: it accepts the no-auth greeting,
// answers CONNECT and pipes bytes between the client and the requested target.
// ---------------------------------------------------------------------------

type socks5TestServer struct {
	ln       net.Listener
	addr     string
	mu       sync.Mutex
	connects []string          // requested CONNECT targets ("host:port")
	resolve  map[string]string // optional target -> real address to dial
}

func newSocks5TestServer(t *testing.T) *socks5TestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &socks5TestServer{ln: ln, addr: ln.Addr().String()}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *socks5TestServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *socks5TestServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil { // VER, NMETHODS
		return
	}
	if buf[0] != 0x05 {
		return
	}
	if _, err := io.ReadFull(conn, buf[:int(buf[1])]); err != nil { // methods
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil { // no auth
		return
	}

	if _, err := io.ReadFull(conn, buf[:4]); err != nil { // VER, CMD, RSV, ATYP
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain name
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		n := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:n]); err != nil {
			return
		}
		host = string(buf[:n])
	default:
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil { // port
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(buf[:2]))))

	s.mu.Lock()
	s.connects = append(s.connects, target)
	s.mu.Unlock()

	dialAddr := target
	if resolved := s.resolve[target]; resolved != "" {
		dialAddr = resolved
	}

	up, err := net.Dial("tcp", dialAddr)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(up, conn) }()
	_, _ = io.Copy(conn, up)
}

func (s *socks5TestServer) targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.connects...)
}

func targetHost(t *testing.T, u string) string {
	t.Helper()
	return strings.TrimPrefix(u, "http://")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewDialContextEmptyIsDirect(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer probe.Close()

	d := NewDialContext("")
	require.NotNil(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := d(ctx, "tcp", targetHost(t, probe.URL))
	require.NoError(t, err)
	_ = conn.Close()
}

func TestDialViaSOCKS5(t *testing.T) {
	socks := newSocks5TestServer(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()

	d := NewDialContext(socks.addr)
	require.NotNil(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d(ctx, "tcp", targetHost(t, target.URL))
	require.NoError(t, err)
	_ = conn.Close()

	require.Contains(t, socks.targets(), targetHost(t, target.URL))
}

func TestDialViaSOCKS5UnreachableProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	dead := ln.Addr().String()
	_ = ln.Close() // nobody listens

	d := NewDialContext(dead)
	require.NotNil(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = d(ctx, "tcp", "127.0.0.1:1")
	require.Error(t, err)
}

func TestPoolHealthyViaSOCKS5(t *testing.T) {
	socks := newSocks5TestServer(t)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{
		{ID: uuidFromBytes(1), Url: ok.URL, Status: "active"},
		{ID: uuidFromBytes(2), Url: "http://127.0.0.1:1", Status: "active"},
	}
	pool := &Pool{Queries: q, CheckTimeout: 2 * time.Second, Dial: NewDialContext(socks.addr)}

	require.True(t, pool.Healthy(q.proxies[0]))
	require.False(t, pool.Healthy(q.proxies[1]))
	require.Contains(t, socks.targets(), targetHost(t, ok.URL))
}

func TestProxyForwardViaSOCKS5(t *testing.T) {
	socks := newSocks5TestServer(t)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("via-socks"))
	}))
	defer upstream.Close()

	q := newFakeQuerier()
	q.proxies = []db.ProxyPool{{ID: uuidFromBytes(1), Url: upstream.URL, Status: "active"}}
	handler := NewHandler(q, newFakeStore(), &Pool{Queries: q}, NewDialContext(socks.addr))
	handler.TargetRewrite = func(path string) string { return strings.TrimPrefix(path, "/tg") }

	id := uuidFromBytes(9)
	q.addUser(id, testUser(id))
	q.consented[id.String()] = true
	sid, err := handler.Sessions.Create(context.Background(), id.String(), time.Hour)
	require.NoError(t, err)

	resp := gatedRequest(handler, sid, http.MethodGet, "/tg/page/42", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "via-socks", readBody(t, resp))
	require.Equal(t, "/page/42", gotPath)
	require.Contains(t, socks.targets(), targetHost(t, upstream.URL))
}
