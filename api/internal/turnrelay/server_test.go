package turnrelay

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Minimal SOCKS5 server with UDP ASSOCIATE (RFC 1928 §7): every association
// gets its own UDP relay socket that unwraps client datagrams, sends them to
// DST and wraps the replies back to the client.
// ---------------------------------------------------------------------------

type socksUDPServer struct {
	ln           net.Listener
	associations atomic.Int32
}

func newSocksUDPServer(t *testing.T) *socksUDPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &socksUDPServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *socksUDPServer) addr() string { return s.ln.Addr().String() }

func (s *socksUDPServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *socksUDPServer) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	if _, err := io.ReadFull(c, buf[:int(buf[1])]); err != nil {
		return
	}
	_, _ = c.Write([]byte{0x05, 0x00})
	if _, err := io.ReadFull(c, buf[:10]); err != nil || buf[1] != 0x03 {
		return
	}

	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	defer func() { _ = relay.Close() }()
	s.associations.Add(1)

	rp := relay.LocalAddr().(*net.UDPAddr)
	reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(reply[8:], uint16(rp.Port))
	_, _ = c.Write(reply)

	go func() {
		var client *net.UDPAddr
		pkt := make([]byte, 65535)
		for {
			n, from, err := relay.ReadFromUDP(pkt)
			if err != nil {
				return
			}
			if from.IP.IsLoopback() && (client == nil || from.String() == client.String()) && n > 10 && pkt[3] == 0x01 && isClientDatagram(pkt[:n], from, client) {
				client = from
				dst := &net.UDPAddr{IP: net.IP(pkt[4:8]), Port: int(binary.BigEndian.Uint16(pkt[8:10]))}
				_, _ = relay.WriteToUDP(pkt[10:n], dst)
				continue
			}
			if client == nil {
				continue
			}
			wrapped := appendSocksUDPHeader(nil, from)
			_, _ = relay.WriteToUDP(append(wrapped, pkt[:n]...), client)
		}
	}()

	_, _ = io.Copy(io.Discard, c) // association lives as long as the control conn
}

// isClientDatagram tells the SOCKS client's wrapped datagrams from peer
// replies: once the client is known, only its address sends wrapped packets.
func isClientDatagram(pkt []byte, from, client *net.UDPAddr) bool {
	if client != nil {
		return from.String() == client.String()
	}
	return pkt[0] == 0 && pkt[1] == 0 && pkt[2] == 0
}

// stunResponder answers Binding requests with the sender's address.
func stunResponder(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			req := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if req.Decode() != nil {
				continue
			}
			res := stun.MustBuild(req, stun.BindingSuccess,
				&stun.XORMappedAddress{IP: from.IP, Port: from.Port})
			_, _ = pc.WriteToUDP(res.Raw, from)
		}
	}()
	return pc.LocalAddr().String()
}

// udpEcho is the call peer: it answers every datagram with "echo:"+payload.
func udpEcho(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(append([]byte("echo:"), buf[:n]...), from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func startRelay(t *testing.T, socks string, mutate func(*Config)) *Server {
	t.Helper()
	cfg := Config{
		ListenAddr: "127.0.0.1:0",
		Realm:      "test",
		Secret:     "s3cret",
		SocksAddr:  socks,
		STUNServer: stunResponder(t),
		Logger:     log.New(io.Discard, "", 0),
		AllowPeer:  func(net.IP) bool { return true },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := Start(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func dialTURN(t *testing.T, srv *Server, user, pass string) *turn.Client {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr().String())
	require.NoError(t, err)
	client, err := turn.NewClient(&turn.ClientConfig{
		TURNServerAddr: srv.Addr().String(),
		Conn:           turn.NewSTUNConn(conn),
		Username:       user,
		Password:       pass,
		Realm:          "test",
	})
	require.NoError(t, err)
	require.NoError(t, client.Listen())
	t.Cleanup(client.Close)
	return client
}

func TestRelayForwardsThroughSocksUDP(t *testing.T) {
	socks := newSocksUDPServer(t)
	srv := startRelay(t, socks.addr(), nil)
	peer := udpEcho(t)

	user, pass, err := Credentials("s3cret", "user-1", time.Hour)
	require.NoError(t, err)
	client := dialTURN(t, srv, user, pass)

	relayConn, err := client.Allocate()
	require.NoError(t, err)
	defer func() { _ = relayConn.Close() }()

	// The advertised relay address is the association's egress as seen by
	// the STUN server, i.e. the SOCKS relay socket, not the local socket.
	relayed := relayConn.LocalAddr().(*net.UDPAddr)
	require.True(t, relayed.IP.IsLoopback())
	require.EqualValues(t, 1, socks.associations.Load())

	_, err = relayConn.WriteTo([]byte("ping"), peer)
	require.NoError(t, err)

	buf := make([]byte, 1500)
	require.NoError(t, relayConn.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, from, err := relayConn.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "echo:ping", string(buf[:n]))
	require.Equal(t, peer.String(), from.String())
}

func TestRelayRejectsBadCredentials(t *testing.T) {
	srv := startRelay(t, newSocksUDPServer(t).addr(), nil)

	user, _, err := Credentials("s3cret", "user-1", time.Hour)
	require.NoError(t, err)
	client := dialTURN(t, srv, user, "wrong-password")

	_, err = client.Allocate()
	require.Error(t, err)
}

func TestRelayRejectsExpiredCredentials(t *testing.T) {
	srv := startRelay(t, newSocksUDPServer(t).addr(), nil)

	user, pass, err := Credentials("s3cret", "user-1", -time.Minute)
	require.NoError(t, err)
	client := dialTURN(t, srv, user, pass)

	_, err = client.Allocate()
	require.Error(t, err)
}

func TestRelayFallsBackToPublicIPWithoutSTUN(t *testing.T) {
	srv := startRelay(t, newSocksUDPServer(t).addr(), func(c *Config) {
		c.STUNServer = "127.0.0.1:1" // nothing answers
		c.PublicIP = net.IPv4(203, 0, 113, 7)
	})

	user, pass, err := Credentials("s3cret", "user-1", time.Hour)
	require.NoError(t, err)
	client := dialTURN(t, srv, user, pass)

	relayConn, err := client.Allocate()
	require.NoError(t, err)
	defer func() { _ = relayConn.Close() }()
	require.Equal(t, "203.0.113.7", relayConn.LocalAddr().(*net.UDPAddr).IP.String())
}

func TestRelayDefaultPermissionBlocksPrivatePeers(t *testing.T) {
	srv := startRelay(t, newSocksUDPServer(t).addr(), func(c *Config) { c.AllowPeer = nil })
	peer := udpEcho(t)

	user, pass, err := Credentials("s3cret", "user-1", time.Hour)
	require.NoError(t, err)
	client := dialTURN(t, srv, user, pass)

	relayConn, err := client.Allocate()
	require.NoError(t, err)
	defer func() { _ = relayConn.Close() }()

	// CreatePermission for 127.0.0.1 is refused, so the write fails.
	_, err = relayConn.WriteTo([]byte("ping"), peer)
	require.Error(t, err)
}

func TestIsPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"149.154.167.51": true,
		"91.108.56.100":  true,
		"10.0.0.1":       false,
		"192.168.0.10": false,
		"172.18.0.1":     false,
		"127.0.0.1":      false,
		"169.254.1.1":    false,
		"100.64.0.1":     false,
		"0.0.0.0":        false,
		"::1":            false,
		"fe80::1":        false,
	} {
		require.Equal(t, want, isPublicIP(net.ParseIP(ip)), ip)
	}
}

func TestSocksUDPHeaderRoundTrip(t *testing.T) {
	for _, a := range []*net.UDPAddr{
		{IP: net.IPv4(149, 154, 167, 51), Port: 443},
		{IP: net.ParseIP("2001:67c:4e8:f004::9"), Port: 3478},
	} {
		pkt := append(appendSocksUDPHeader(nil, a), []byte("data")...)
		payload, src, ok := parseSocksUDP(pkt)
		require.True(t, ok)
		require.Equal(t, "data", string(payload))
		require.Equal(t, a.String(), src.String())
	}

	_, _, ok := parseSocksUDP([]byte{0, 0, 1, 0x01, 1, 2, 3, 4, 0, 1}) // FRAG != 0
	require.False(t, ok)
	_, _, ok = parseSocksUDP([]byte{0, 0, 0, 0x03, 3, 'a', 'b', 'c', 0, 1}) // domain
	require.False(t, ok)
}

func TestFromRelayAcceptsDockerBridgeSource(t *testing.T) {
	c := &socksPacketConn{relay: &net.UDPAddr{IP: net.IPv4(192, 168, 31, 138), Port: 20170}}
	require.True(t, c.fromRelay(&net.UDPAddr{IP: net.IPv4(192, 168, 31, 138), Port: 20170}))
	require.True(t, c.fromRelay(&net.UDPAddr{IP: net.IPv4(172, 25, 0, 1), Port: 20170}))
	require.False(t, c.fromRelay(&net.UDPAddr{IP: net.IPv4(172, 25, 0, 1), Port: 20171}))
	require.False(t, c.fromRelay(&net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 20170}))
}
