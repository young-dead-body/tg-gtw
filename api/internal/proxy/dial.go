package proxy

import (
	"context"
	"net"
	"time"

	"golang.org/x/net/proxy"
)

// DialContext connects to (network, addr), optionally tunnelling the
// connection through a SOCKS5 proxy. addr for a TCP network may be a hostname:
// with a SOCKS5 dialer the name is resolved at the proxy (SOCKS5a/RFC1928),
// so the target is reachable even when the gateway itself cannot resolve or
// route to it (telegram.org behind a corporate firewall).
type DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

// NewDialContext builds a DialContext. When socks5Addr is empty it dials
// directly; otherwise every new connection is opened through the SOCKS5
// endpoint (used for both client traffic and backend health probes).
func NewDialContext(socks5Addr string) DialContext {
	if socks5Addr == "" {
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext
	}
	d, err := proxy.SOCKS5("tcp", socks5Addr, nil, proxy.Direct)
	if err != nil {
		return nil
	}
	if cd, ok := d.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}); ok {
		return cd.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.Dial(network, addr)
	}
}
