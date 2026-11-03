package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errNoEgress = errors.New("no egress")

func startDCProxy(t *testing.T, dcID int, dial DialContext, insecure bool) net.Listener {
	t.Helper()
	p := NewDCProxy("127.0.0.1:0", dcID, dial, nil)
	p.Insecure = insecure
	ln, err := p.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = p.Serve(ln) }()
	return ln
}

func TestDCHosts(t *testing.T) {
	require.Equal(t, "kws1.web.telegram.org", DCHosts[1])
	require.Equal(t, "kws2.web.telegram.org", DCHosts[2])
	require.Equal(t, "kws3.web.telegram.org", DCHosts[3])
	require.Equal(t, "kws4.web.telegram.org", DCHosts[4])
	require.Equal(t, "kws5.web.telegram.org", DCHosts[5])
}

func TestDCProxySplicesThroughSOCKS(t *testing.T) {
	// Fake real DC: TLS server that greets and answers lines, exactly like
	// the Telegram DC would behind its official certificate.
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
				if _, err := tc.Write([]byte("welcome-dc\n")); err != nil {
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
		"kws1.web.telegram.org:443": upLn.Addr().String(),
	}

	ln := startDCProxy(t, 1, NewDialContext(socks.addr), true)

	// The browser side of the bridge: plaintext TCP (nginx already terminated
	// TLS on port 20001 and forwarded here).
	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	rd := bufio.NewReader(client)

	line, err := rd.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "welcome-dc\n", line)

	_, err = client.Write([]byte("ping\n"))
	require.NoError(t, err)

	line, err = rd.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong:ping\n", line)

	require.Contains(t, socks.targets(), "kws1.web.telegram.org:443")
}

func TestDCProxyDialErrorClosesClient(t *testing.T) {
	ln := startDCProxy(t, 1, func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errNoEgress
	}, true)

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	buf := make([]byte, 1)
	_, err = client.Read(buf) // peer closes after dial failure
	require.Error(t, err)
}

func TestDCProxyTLSVerificationByDefault(t *testing.T) {
	// With Insecure=false the bridge must verify the DC certificate chain,
	// so a self-signed fake DC must be refused. Dial must still happen.
	socks := newSocks5TestServer(t)
	socks.resolve = map[string]string{
		"kws1.web.telegram.org:443": "127.0.0.1:1", // dial succeeds, TLS fails
	}

	p := NewDCProxy("127.0.0.1:0", 1, NewDialContext(socks.addr), nil)
	ln, err := p.Listen()
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() { _ = p.Serve(ln) }()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	buf := make([]byte, 1)
	_, err = client.Read(buf) // connection closes: TLS verification failed
	require.Error(t, err)
}
