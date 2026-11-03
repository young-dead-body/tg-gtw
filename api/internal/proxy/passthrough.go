package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// PassthroughAddrDefault is the default listen address of the TLS passthrough
// that carries the Telegram Web client's DC connections.
const PassthroughAddrDefault = ":9443"

const (
	passthroughPeekTimeout = 15 * time.Second
	passthroughDialTimeout = 10 * time.Second
	passthroughBufSize     = 1 << 15
)

var (
	errNotTLS    = errors.New("not a TLS handshake record")
	errNoSNI     = errors.New("no server_name in ClientHello")
	errBadHello  = errors.New("malformed ClientHello")
	errBadRecord = errors.New("malformed TLS record")
)

// Passthrough terminates nothing: it sniffs the TLS SNI from the client
// connection, and when it is a Telegram Web DC host (*.web.telegram.org) it
// tunnels the raw TLS stream to that host through the configured DialContext
// (SOCKS5). TLS stays end-to-end between the browser and Telegram, so no
// certificates are involved. Any other SNI is refused. This is the transport
// that keeps the browser's MTProto connections inside the gateway even though
// the Telegram Web client hardcodes https://*.web.telegram.org endpoints.
type Passthrough struct {
	Addr   string
	Dial   DialContext
	Logger *log.Logger
}

// NewPassthrough builds a Passthrough. An empty addr selects the default
// listen address; a nil Logger selects log.Default().
func NewPassthrough(addr string, dial DialContext, logger *log.Logger) *Passthrough {
	if addr == "" {
		addr = PassthroughAddrDefault
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Passthrough{Addr: addr, Dial: dial, Logger: logger}
}

// Listen opens the TCP listener.
func (p *Passthrough) Listen() (net.Listener, error) {
	return net.Listen("tcp", p.Addr)
}

// Serve accepts connections on ln until it is closed or errors.
func (p *Passthrough) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handle(conn)
	}
}

// ListenAndServe opens the listener and serves until it errors.
func (p *Passthrough) ListenAndServe() error {
	ln, err := p.Listen()
	if err != nil {
		return err
	}
	p.Logger.Printf("telegram TLS passthrough listening on %s", p.Addr)
	return p.Serve(ln)
}

// handle peeks the SNI, dials the real Telegram DC host through the egress
// dialer and splices raw bytes in both directions.
func (p *Passthrough) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(passthroughPeekTimeout)); err != nil {
		return
	}

	br := bufio.NewReaderSize(conn, passthroughBufSize)
	raw, host, err := peekSNI(br)
	if err != nil {
		p.Logger.Printf("passthrough: %s: %v", conn.RemoteAddr(), err)
		return
	}
	if !isTelegramWebHost(host) {
		p.Logger.Printf("passthrough: %s: refused SNI %q", conn.RemoteAddr(), host)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), passthroughDialTimeout)
	defer cancel()
	up, err := p.Dial(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		p.Logger.Printf("passthrough: dial %s: %v", host, err)
		return
	}
	defer func() { _ = up.Close() }()

	// The peeked ClientHello bytes were consumed from the socket and must be
	// replayed verbatim before the live stream (io.ReadFull already removed
	// them from the buffer).
	_ = conn.SetDeadline(time.Time{})
	p.Logger.Printf("passthrough: %s -> %s", conn.RemoteAddr(), host)
	splice(conn, up, io.MultiReader(bytes.NewReader(raw), br))
}

// isTelegramWebHost reports whether host is web.telegram.org or one of its
// subdomains (the Telegram Web client's DC transport hosts: venus, aurora,
// pluto, vesta, flora, ...).
func isTelegramWebHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "web.telegram.org" {
		return true
	}
	return strings.HasSuffix(host, ".web.telegram.org")
}

// peekSNI reads a (possibly multi-record) TLS ClientHello from br and extracts
// the SNI host name. It returns the exact raw bytes consumed from the socket
// so the caller can replay the ClientHello to the real server untouched.
func peekSNI(br *bufio.Reader) (raw []byte, host string, err error) {
	raw, hello, err := readClientHello(br)
	if err != nil {
		return nil, "", err
	}
	host, err = parseSNI(hello)
	if err != nil {
		return nil, "", err
	}
	return raw, host, nil
}

// readClientHello accumulates TLS records until the full ClientHello
// handshake message has been read, returning both the assembled message and
// every raw byte consumed (headers included) for exact replay.
func readClientHello(br *bufio.Reader) (raw, hello []byte, err error) {
	for {
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(br, hdr); err != nil {
			return nil, nil, err
		}
		if hdr[0] != 0x16 { // handshake record
			return nil, nil, errNotTLS
		}
		n := int(hdr[3])<<8 | int(hdr[4])
		if n < 4 || n > 1<<14 {
			return nil, nil, errBadRecord
		}
		rec := make([]byte, n)
		if _, err := io.ReadFull(br, rec); err != nil {
			return nil, nil, err
		}
		raw = append(raw, hdr...)
		raw = append(raw, rec...)
		hello = append(hello, rec...)
		if len(hello) >= 4 {
			mlen := int(hello[1])<<16 | int(hello[2])<<8 | int(hello[3])
			if len(hello) >= mlen+4 {
				return raw, hello[:mlen+4], nil
			}
		}
	}
}

// parseSNI walks the ClientHello handshake message and returns the first
// host_name from the server_name extension (RFC 6066), or an error.
func parseSNI(msg []byte) (string, error) {
	// msg: [type 1][length 3][legacy_version 2][random 32]
	if len(msg) < 4 || msg[0] != 0x01 {
		return "", errBadHello
	}
	pos := 4 + 2 + 32

	if pos+1 > len(msg) {
		return "", errBadHello
	}
	sl := int(msg[pos])
	pos += 1 + sl

	if pos+2 > len(msg) {
		return "", errBadHello
	}
	cl := int(msg[pos])<<8 | int(msg[pos+1])
	pos += 2 + cl

	if pos+1 > len(msg) {
		return "", errBadHello
	}
	cml := int(msg[pos])
	pos += 1 + cml

	if pos+2 > len(msg) {
		return "", errBadHello
	}
	el := int(msg[pos])<<8 | int(msg[pos+1])
	pos += 2

	end := pos + el
	if end > len(msg) {
		return "", errBadHello
	}
	for pos+4 <= end {
		etype := int(msg[pos])<<8 | int(msg[pos+1])
		elen := int(msg[pos+2])<<8 | int(msg[pos+3])
		pos += 4
		if pos+elen > end {
			return "", errBadHello
		}
		if etype == 0 { // server_name
			return parseServerName(msg[pos : pos+elen])
		}
		pos += elen
	}
	return "", errNoSNI
}

// parseServerName extracts host_name from the server_name extension payload.
func parseServerName(d []byte) (string, error) {
	if len(d) < 3 {
		return "", errBadHello
	}
	ll := int(d[0])<<8 | int(d[1])
	if ll+2 > len(d) {
		return "", errBadHello
	}
	for p := 2; p+3 <= 2+ll; {
		nt := d[p]
		nl := int(d[p+1])<<8 | int(d[p+2])
		p += 3
		if p+nl > 2+ll {
			return "", errBadHello
		}
		if nt == 0 { // host_name
			return string(d[p : p+nl]), nil
		}
		p += nl
	}
	return "", errNoSNI
}

// splice copies bytes in both directions until either side closes. The client's
// original stream (including the peeked ClientHello) must be provided as r.
func splice(client net.Conn, up net.Conn, rClient io.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, rClient)
		if tcp, ok := up.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, up)
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	wg.Wait()
}
