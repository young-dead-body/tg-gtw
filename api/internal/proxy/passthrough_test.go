package proxy

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testTLSCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"venus.web.telegram.org", "*.web.telegram.org"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startPassthrough(t *testing.T, dial DialContext) net.Listener {
	t.Helper()
	pt := NewPassthrough("127.0.0.1:0", dial, nil)
	ln, err := pt.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = pt.Serve(ln) }()
	return ln
}

func TestIsTelegramWebHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"web.telegram.org", true},
		{"venus.web.telegram.org", true},
		{"pluto.web.telegram.org", true},
		{"pluto-1.web.telegram.org", true},
		{"web.telegram.org.", true},
		{"VENUS.WEB.TELEGRAM.ORG", true},
		{"example.com", false},
		{"evilweb.telegram.org", false},
		{"web.telegram.org.evil.com", false},
		{"telegram.org", false},
		{"", false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, isTelegramWebHost(c.host), c.host)
	}
}

func TestPassthroughForwardsTelegramSNI(t *testing.T) {
	// Fake "Telegram DC" upstream: a TLS server that greets the client and
	// answers any line.
	upLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{testTLSCert(t)},
	})
	require.NoError(t, err)
	defer func() { _ = upLn.Close() }()

	go func() {
		for {
			c, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				tc, ok := conn.(*tls.Conn)
				if !ok {
					return
				}
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if err := tc.Handshake(); err != nil {
					return
				}
				if _, err := tc.Write([]byte("welcome-to-telegram\n")); err != nil {
					return
				}
				line, err := bufio.NewReader(tc).ReadString('\n')
				if err != nil {
					return
				}
				_, _ = tc.Write([]byte("pong:" + strings.TrimSpace(line) + "\n"))
			}(c)
		}
	}()

	socks := newSocks5TestServer(t)
	socks.resolve = map[string]string{
		"venus.web.telegram.org:443": upLn.Addr().String(),
	}

	ln := startPassthrough(t, NewDialContext(socks.addr))

	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName:         "venus.web.telegram.org",
		InsecureSkipVerify: true, // the upstream cert is self-signed in tests
	})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	rd := bufio.NewReader(client)

	line, err := rd.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "welcome-to-telegram\n", line)

	_, err = client.Write([]byte("ping\n"))
	require.NoError(t, err)

	line, err = rd.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong:ping\n", line)

	require.Contains(t, socks.targets(), "venus.web.telegram.org:443")
}

func TestPassthroughGatesInWrongSNI(t *testing.T) {
	called := false
	ln := startPassthrough(t, func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	})

	// Wrong domain: SNI is not a Telegram Web host, so the connection must be
	// refused *before* any outbound dial.
	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName:         "evil.example.com",
		InsecureSkipVerify: true,
	})
	require.Error(t, err)
	if client != nil {
		_ = client.Close()
	}
	require.False(t, called)
}

func TestPassthroughConnectsWithoutSNI(t *testing.T) {
	called := false
	ln := startPassthrough(t, func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	})

	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
	})
	require.Error(t, err)
	if client != nil {
		_ = client.Close()
	}
	require.False(t, called)
}

func TestPassthroughRejectsNonTLS(t *testing.T) {
	called := false
	ln := startPassthrough(t, func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Plain HTTP bytes instead of a TLS ClientHello.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: venus.web.telegram.org\r\n\r\n"))
	require.NoError(t, err)

	buf := make([]byte, 1)
	_, err = conn.Read(buf) // peer closes after non-TLS bytes
	require.Error(t, err)
	require.False(t, called)
}

func TestPassthroughDialErrorIsNotFatal(t *testing.T) {
	ln := startPassthrough(t, func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("socks unreachable")
	})

	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName:         "venus.web.telegram.org",
		InsecureSkipVerify: true,
	})
	require.Error(t, err)
	if client != nil {
		_ = client.Close()
	}
}

func TestParseSNIErrors(t *testing.T) {
	// Garbage / truncated payloads must error out, never panic.
	_, err := parseSNI([]byte{0x02, 0x00, 0x00, 0x00})
	require.Error(t, err)

	_, err = parseSNI([]byte{0x01, 0x00, 0x00, 0x03, 0x03, 0x03}) // truncated hello
	require.Error(t, err)

	// ClientHello with no server_name extension.
	hello := make([]byte, 0, 4+2+32+1+2+1+2)
	hello = append(hello, 0x01) // handshake type
	hello = append(hello, 0x00, 0x00, 0x28)
	hello = append(hello, 0x03, 0x03) // legacy_version
	hello = append(hello, make([]byte, 32)...)
	hello = append(hello, 0x00)                   // session_id_len
	hello = append(hello, 0x00, 0x02, 0x13, 0x01) // cipher_suites (2 bytes)
	hello = append(hello, 0x01, 0x00)             // compression
	hello = append(hello, 0x00, 0x00)             // no extensions
	_, err = parseSNI(hello)
	require.ErrorIs(t, err, errNoSNI)
}
