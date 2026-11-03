package turnrelay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// socksPacketConn is a net.PacketConn whose datagrams travel through a SOCKS5
// UDP ASSOCIATE (RFC 1928 §7). The mini-PC has no direct route to Telegram,
// so the TURN relay socket of every allocation is one such association: the
// peer (Telegram TURN / the other party) sees the SOCKS egress address.
//
// The association lives as long as its TCP control connection; closing the
// PacketConn tears both down, and a dropped control connection closes the
// PacketConn so the allocation fails instead of silently black-holing.
type socksPacketConn struct {
	ctrl  net.Conn     // TCP control connection that keeps the association alive
	udp   *net.UDPConn // local socket talking to the SOCKS UDP relay
	relay *net.UDPAddr // SOCKS server's UDP relay endpoint (BND.ADDR:PORT)

	mu     sync.Mutex
	public net.Addr // egress address as seen from the internet (set by discovery)

	closeOnce sync.Once
}

// dialSocksUDP opens a UDP association on the SOCKS5 server at socksAddr.
func dialSocksUDP(ctx context.Context, socksAddr string) (*socksPacketConn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	ctrl, err := d.DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5 udp: dial %s: %w", socksAddr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = ctrl.SetDeadline(dl)
	} else {
		_ = ctrl.SetDeadline(time.Now().Add(10 * time.Second))
	}

	relay, err := socksAssociate(ctrl)
	if err != nil {
		_ = ctrl.Close()
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Time{})

	// A server answering 0.0.0.0 means "same host as the control connection";
	// a loopback answer from a remote server (a proxy bound to 127.0.0.1 for
	// its own host) is unreachable from here and means the same.
	ctrlIP := ctrl.RemoteAddr().(*net.TCPAddr).IP
	if relay.IP.IsUnspecified() || (relay.IP.IsLoopback() && !ctrlIP.IsLoopback()) {
		relay.IP = ctrlIP
	}

	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		_ = ctrl.Close()
		return nil, fmt.Errorf("socks5 udp: listen: %w", err)
	}

	c := &socksPacketConn{ctrl: ctrl, udp: udp, relay: relay}
	go c.watchControl()
	return c, nil
}

// socksAssociate performs the no-auth greeting and the UDP ASSOCIATE request,
// returning the relay endpoint the server allocated.
func socksAssociate(ctrl net.Conn) (*net.UDPAddr, error) {
	if _, err := ctrl.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, fmt.Errorf("socks5 udp: greeting: %w", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(ctrl, greet[:]); err != nil {
		return nil, fmt.Errorf("socks5 udp: greeting reply: %w", err)
	}
	if greet[0] != 0x05 || greet[1] != 0x00 {
		return nil, fmt.Errorf("socks5 udp: server refused no-auth method (%#x)", greet[1])
	}

	// UDP ASSOCIATE with DST 0.0.0.0:0: the client address is not known up
	// front (docker NAT), so the server must accept datagrams from any source.
	if _, err := ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("socks5 udp: associate: %w", err)
	}
	var hdr [4]byte
	if _, err := io.ReadFull(ctrl, hdr[:]); err != nil {
		return nil, fmt.Errorf("socks5 udp: associate reply: %w", err)
	}
	if hdr[1] != 0x00 {
		return nil, fmt.Errorf("socks5 udp: associate rejected (reply %#x)", hdr[1])
	}
	var ip net.IP
	switch hdr[3] {
	case 0x01:
		ip = make(net.IP, 4)
	case 0x04:
		ip = make(net.IP, 16)
	default:
		return nil, fmt.Errorf("socks5 udp: unsupported BND.ADDR type %#x", hdr[3])
	}
	if _, err := io.ReadFull(ctrl, ip); err != nil {
		return nil, fmt.Errorf("socks5 udp: associate reply addr: %w", err)
	}
	var port [2]byte
	if _, err := io.ReadFull(ctrl, port[:]); err != nil {
		return nil, fmt.Errorf("socks5 udp: associate reply port: %w", err)
	}
	return &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(port[:]))}, nil
}

// watchControl closes the association as soon as the SOCKS server drops the
// TCP control connection (RFC 1928: the association ends with it).
func (c *socksPacketConn) watchControl() {
	_, _ = io.Copy(io.Discard, c.ctrl)
	_ = c.Close()
}

// ReadFrom returns the next datagram from the relay with the SOCKS UDP header
// stripped; addr is the remote peer that sent it.
func (c *socksPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, len(p)+262)
	for {
		n, from, err := c.udp.ReadFromUDP(buf)
		if err != nil {
			return 0, nil, err
		}
		if !c.fromRelay(from) {
			continue
		}
		payload, src, ok := parseSocksUDP(buf[:n])
		if !ok {
			continue
		}
		return copy(p, payload), src, nil
	}
}

// fromRelay reports whether a datagram came from the SOCKS relay. Only the
// relay may inject datagrams into the association, but a relay on the docker
// host answers from the bridge address (172.x.0.1) rather than the LAN IP it
// was reached at, so any private/loopback source on the relay port counts.
func (c *socksPacketConn) fromRelay(from *net.UDPAddr) bool {
	if from.Port != c.relay.Port {
		return false
	}
	return from.IP.Equal(c.relay.IP) || from.IP.IsPrivate() || from.IP.IsLoopback()
}

// WriteTo sends p to addr through the relay.
func (c *socksPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("socks5 udp: unsupported address %T", addr)
	}
	pkt := appendSocksUDPHeader(make([]byte, 0, len(p)+22), ua)
	pkt = append(pkt, p...)
	if _, err := c.udp.WriteToUDP(pkt, c.relay); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *socksPacketConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = errors.Join(c.udp.Close(), c.ctrl.Close())
	})
	return err
}

// LocalAddr is the public egress address once discovered, so the TURN server
// advertises an address the peer can actually reach (TURN permissions are
// IP-based, so the IP is what matters; the port may be remapped by the
// egress NAT and is then learnt by ICE as a peer-reflexive candidate).
func (c *socksPacketConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.public != nil {
		return c.public
	}
	return c.udp.LocalAddr()
}

func (c *socksPacketConn) setPublic(a net.Addr) {
	c.mu.Lock()
	c.public = a
	c.mu.Unlock()
}

func (c *socksPacketConn) SetDeadline(t time.Time) error      { return c.udp.SetDeadline(t) }
func (c *socksPacketConn) SetReadDeadline(t time.Time) error  { return c.udp.SetReadDeadline(t) }
func (c *socksPacketConn) SetWriteDeadline(t time.Time) error { return c.udp.SetWriteDeadline(t) }

// appendSocksUDPHeader appends RSV(2) FRAG(1) ATYP DST.ADDR DST.PORT.
func appendSocksUDPHeader(b []byte, a *net.UDPAddr) []byte {
	b = append(b, 0, 0, 0)
	if ip4 := a.IP.To4(); ip4 != nil {
		b = append(b, 0x01)
		b = append(b, ip4...)
	} else {
		b = append(b, 0x04)
		b = append(b, a.IP.To16()...)
	}
	return binary.BigEndian.AppendUint16(b, uint16(a.Port))
}

// parseSocksUDP splits a relay datagram into payload and source address.
// Fragmented datagrams (FRAG != 0) are dropped, as RFC 1928 allows.
func parseSocksUDP(b []byte) ([]byte, *net.UDPAddr, bool) {
	if len(b) < 4 || b[2] != 0 {
		return nil, nil, false
	}
	var ip net.IP
	off := 4
	switch b[3] {
	case 0x01:
		if len(b) < off+4+2 {
			return nil, nil, false
		}
		ip = net.IP(append([]byte(nil), b[off:off+4]...))
		off += 4
	case 0x04:
		if len(b) < off+16+2 {
			return nil, nil, false
		}
		ip = net.IP(append([]byte(nil), b[off:off+16]...))
		off += 16
	case 0x03:
		// Domain source addresses are not expected for relayed replies; the
		// TURN permission check needs an IP, so such datagrams are dropped.
		return nil, nil, false
	default:
		return nil, nil, false
	}
	port := int(binary.BigEndian.Uint16(b[off : off+2]))
	return b[off+2:], &net.UDPAddr{IP: ip, Port: port}, true
}
