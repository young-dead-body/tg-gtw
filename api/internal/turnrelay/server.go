// Package turnrelay is the gateway's TURN server for Telegram Web calls.
//
// Call signalling already reaches Telegram through the DC bridge, but the
// call media is WebRTC: the browser has to exchange UDP with Telegram's TURN
// servers or the other party, and neither is reachable from the client
// networks the gateway serves. The relay closes that gap:
//
//	browser --turns:443 (TCP/TLS)--> VPS nginx (SNI) --frp--> this server
//	        --UDP via SOCKS5 UDP ASSOCIATE--> Telegram TURN / the peer
//
// TLS is terminated on the VPS, so this server speaks plain TURN over TCP.
// Each allocation's relay socket is its own SOCKS5 UDP association, and the
// advertised relay address is the association's public egress address found
// with a STUN Binding request sent through it.
package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
)

// Config describes the relay.
type Config struct {
	// ListenAddr is the TCP address TURN clients connect to (e.g. ":3478").
	ListenAddr string
	// Realm is the TURN realm (any stable string, e.g. the public domain).
	Realm string
	// Secret is the shared secret of the TURN REST credentials
	// (username "<expiry>:<user>", password base64(HMAC-SHA1(secret, username))).
	Secret string
	// SocksAddr is the SOCKS5 server with UDP ASSOCIATE support used as egress.
	SocksAddr string
	// STUNServer discovers the egress address of each association
	// ("host:port"). Empty disables discovery; PublicIP is then required.
	STUNServer string
	// PublicIP is the fallback relay IP when STUN discovery fails.
	PublicIP net.IP
	// Logger receives allocation diagnostics; nil uses log.Default().
	Logger *log.Logger
	// AllowPeer filters the peers a client may relay to; nil admits only
	// public unicast addresses (see publicPeerOnly).
	AllowPeer func(peer net.IP) bool
}

// Server wraps a running pion TURN server.
type Server struct {
	turn *turn.Server
	ln   net.Listener
}

// Start listens on cfg.ListenAddr and serves TURN until Close.
func Start(cfg Config) (*Server, error) {
	if cfg.Secret == "" {
		return nil, errors.New("turnrelay: empty secret")
	}
	if cfg.SocksAddr == "" {
		return nil, errors.New("turnrelay: SOCKS5 egress is required")
	}
	if cfg.STUNServer == "" && cfg.PublicIP == nil {
		return nil, errors.New("turnrelay: need a STUN server or a public IP to advertise")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("turnrelay: listen %s: %w", cfg.ListenAddr, err)
	}

	permit := turn.PermissionHandler(publicPeerOnly)
	if cfg.AllowPeer != nil {
		allow := cfg.AllowPeer
		permit = func(_ net.Addr, peer net.IP) bool { return allow(peer) }
	}

	loggers := logging.NewDefaultLoggerFactory()
	loggers.DefaultLogLevel = logging.LogLevelWarn

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         cfg.Realm,
		AuthHandler:   turn.LongTermTURNRESTAuthHandler(cfg.Secret, loggers.NewLogger("turn")),
		LoggerFactory: loggers,
		ListenerConfigs: []turn.ListenerConfig{{
			Listener:              ln,
			RelayAddressGenerator: &socksRelayGenerator{cfg: cfg},
			PermissionHandler:     permit,
		}},
	})
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("turnrelay: %w", err)
	}
	return &Server{turn: srv, ln: ln}, nil
}

// Addr is the listening address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the server and releases every allocation.
func (s *Server) Close() error { return s.turn.Close() }

// Credentials issues TURN REST credentials for user, valid for ttl.
func Credentials(secret, user string, ttl time.Duration) (username, password string, err error) {
	return turn.GenerateLongTermTURNRESTCredentials(secret, user, ttl)
}

// publicPeerOnly keeps the relay from being used to reach private, loopback
// or link-local addresses: the peers of a Telegram call are public hosts.
func publicPeerOnly(_ net.Addr, peer net.IP) bool {
	return isPublicIP(peer)
}

func isPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !isCGNAT(ip)
}

// isCGNAT reports 100.64.0.0/10 (shared address space, RFC 6598).
func isCGNAT(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64
}

// socksRelayGenerator allocates each relay socket as a SOCKS5 UDP association.
type socksRelayGenerator struct {
	cfg Config
}

func (g *socksRelayGenerator) Validate() error { return nil }

func (g *socksRelayGenerator) AllocatePacketConn(network string, _ int) (net.PacketConn, net.Addr, error) {
	if !strings.HasPrefix(network, "udp") {
		return nil, nil, fmt.Errorf("turnrelay: unsupported relay network %q", network)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dialSocksUDP(ctx, g.cfg.SocksAddr)
	if err != nil {
		g.cfg.Logger.Printf("turnrelay: allocate: %v", err)
		return nil, nil, err
	}

	public, err := g.discover(conn)
	if err != nil {
		if g.cfg.PublicIP == nil {
			_ = conn.Close()
			g.cfg.Logger.Printf("turnrelay: allocate: egress discovery: %v", err)
			return nil, nil, err
		}
		g.cfg.Logger.Printf("turnrelay: egress discovery failed (%v), advertising %s", err, g.cfg.PublicIP)
		public = &net.UDPAddr{IP: g.cfg.PublicIP, Port: conn.relay.Port}
	}
	conn.setPublic(public)
	g.cfg.Logger.Printf("turnrelay: allocation relayed via SOCKS5 egress %s", public)
	return conn, public, nil
}

func (g *socksRelayGenerator) AllocateConn(string, int) (net.Conn, net.Addr, error) {
	return nil, nil, errors.New("turnrelay: TCP relay allocations are not supported")
}

// discover sends a STUN Binding request through the association and returns
// the XOR-MAPPED-ADDRESS, i.e. the association's public egress address.
func (g *socksRelayGenerator) discover(conn *socksPacketConn) (*net.UDPAddr, error) {
	if g.cfg.STUNServer == "" {
		return nil, errors.New("no STUN server configured")
	}
	server, err := net.ResolveUDPAddr("udp4", g.cfg.STUNServer)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", g.cfg.STUNServer, err)
	}

	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	buf := make([]byte, 1500)
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	// UDP through the tunnel may drop a packet: retry a few times.
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := conn.WriteTo(req.Raw, server); err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break
				}
				return nil, err
			}
			res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if res.Decode() != nil || res.TransactionID != req.TransactionID {
				continue
			}
			var xor stun.XORMappedAddress
			if err := xor.GetFrom(res); err != nil {
				return nil, fmt.Errorf("binding response: %w", err)
			}
			return &net.UDPAddr{IP: xor.IP, Port: xor.Port}, nil
		}
	}
	return nil, fmt.Errorf("no STUN response from %s", g.cfg.STUNServer)
}
