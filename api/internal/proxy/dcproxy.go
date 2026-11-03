package proxy

import (
	"context"
	"log"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// DCProxyAddrDefault is the default listen address of the first DC bridge.
const DCProxyAddrDefault = ":9451"

const (
	dcProxyDialTimeout = 10 * time.Second
	dcProxyIdleTimeout = 10 * time.Minute
)

// DCHosts maps Telegrams datacenter IDs 1..5 to their WebSocket fronting
// hosts. Telegram Web K builds its bootstrap MTProto endpoint as
// wss://kws<dc>.web.telegram.org/apiws (Sec-WebSocket-Protocol: binary), and
// the edge routes apiws traffic by TLS SNI / Host matching this name: the
// TCP-MTProto fronting hosts (venus/aurora/vesta/pluto/flora) answer the WS
// handshake with 302/404. The DC bridge re-establishes the same TLS session
// on the *real* host through the egress SOCKS5 dialer; the entry nginx must
// forward the handshake with Host matching kws<dc> (see the VPS dc-vhost).
var DCHosts = map[int]string{
	1: "kws1.web.telegram.org",
	2: "kws2.web.telegram.org",
	3: "kws3.web.telegram.org",
	4: "kws4.web.telegram.org",
	5: "kws5.web.telegram.org",
}

// DCProxy is the server side of the DC bridge: nginx terminates the browser's
// TLS on ports 20001..20005 and forwards the plaintext stream here (one
// listener per DC). The bridge performs a *second* TLS connection to the real
// Telegram DC fronting host through the egress dialer and splices the streams,
// so the browser's MTProto bytes reach Telegram with proper end-to-end TLS.
type DCProxy struct {
	// Addr is the TCP listen address (one DCProxy per listener).
	Addr string
	// DCID is the Telegram datacenter ID served by this listener.
	DCID int
	// Host overrides the DC fronting host (defaults to DCHosts[DCID]).
	Host string
	// Dial is the egress dialer (SOCKS5); nil dials directly.
	Dial DialContext
	// Insecure skips TLS verification of the real DC (tests only).
	Insecure bool
	Logger   *log.Logger
}

// NewDCProxy builds a DC bridge; empty addr selects the default address.
func NewDCProxy(addr string, dcID int, dial DialContext, logger *log.Logger) *DCProxy {
	if addr == "" {
		addr = DCProxyAddrDefault
	}
	if logger == nil {
		logger = log.Default()
	}
	return &DCProxy{Addr: addr, DCID: dcID, Dial: dial, Logger: logger}
}

// Listen opens the TCP listener.
func (p *DCProxy) Listen() (net.Listener, error) {
	return net.Listen("tcp", p.Addr)
}

// Serve accepts connections until the listener closes or errors.
func (p *DCProxy) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handle(conn)
	}
}

// ListenAndServe opens the listener and serves until it errors.
func (p *DCProxy) ListenAndServe() error {
	ln, err := p.Listen()
	if err != nil {
		return err
	}
	p.Logger.Printf("DC bridge listening on %s (dc=%d -> %s)", p.Addr, p.DCID, p.host())
	return p.Serve(ln)
}

func (p *DCProxy) host() string {
	if p.Host != "" {
		return p.Host
	}
	return DCHosts[p.DCID]
}

// handle dials the real DC through the egress dialer, wraps it in TLS and
// splices both directions.
func (p *DCProxy) handle(client net.Conn) {
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), dcProxyDialTimeout)
	defer cancel()
	up, err := p.Dial(ctx, "tcp", net.JoinHostPort(p.host(), "443"))
	if err != nil {
		p.Logger.Printf("dcproxy(dc=%d): dial %s: %v", p.DCID, p.host(), err)
		return
	}

	// Second leg of the double TLS: browser↔nginx (+ self-signed) and us↔real
	// DC (official cert, validated normally). The upstream leg must present a
	// *browser-like* TLS ClientHello (uTLS, Chrome fingerprint): the apiws
	// edge fingerprints the ClientHello (JA3/JA4) and rejects MTProto sessions
	// opened from non-browser TLS stacks (Go crypto/tls) with a transport
	// "404" right after the obfuscated init — the browser bundle, WS upgrade
	// and MTProto bytes are relayed verbatim, only the TLS fingerprint
	// differs, and that alone kills the connection (QR login hangs).
	// ALPN is pinned to http/1.1 (overriding the Chrome preset's [h2,
	// http/1.1] — otherwise h2 is negotiated and the HTTP/1.1 WebSocket
	// upgrade splice desyncs with "Invalid status line").
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_120)
	if err != nil {
		p.Logger.Printf("dcproxy(dc=%d): client hello spec: %v", p.DCID, err)
		_ = up.Close()
		return
	}
	for i, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
			spec.Extensions[i] = alpn
		}
	}
	uconn := utls.UClient(up, &utls.Config{
		ServerName:         p.host(),
		InsecureSkipVerify: p.Insecure,
	}, utls.HelloCustom)
	if err := uconn.ApplyPreset(&spec); err != nil {
		p.Logger.Printf("dcproxy(dc=%d): TLS preset: %v", p.DCID, err)
		_ = up.Close()
		return
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		p.Logger.Printf("dcproxy(dc=%d): TLS handshake with %s: %v", p.DCID, p.host(), err)
		_ = up.Close()
		return
	}
	p.Logger.Printf("dcproxy: dc=%d client %s -> %s", p.DCID, client.RemoteAddr(), p.host())

	_ = client.SetDeadline(time.Time{})
	splice(client, uconn, client)
}
