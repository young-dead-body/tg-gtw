package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// TL constructors used by the Telegram config / dcOption serialization
// (little-endian wire format).
const (
	// config#330b4067 flags:# ... this_dc:int dc_options:Vector<dcOption>
	configCtorLE = 0x330b4067
	// dcOption#5d8c6cc flags:# id:int ip_address:string port:int secret:flags.10?bytes
	dcOptionCtorLE    = 0x5d8c6cc
	dcOptionSecretBit = 1 << 10

	// Flags that make an entry unsuitable for IP rewriting: IPv6 addresses,
	// CDN/media-only nodes are not tunnelled through the DC bridge.
	dcOptionIPv6      = 1 << 0
	dcOptionMediaOnly = 1 << 1
	dcOptionCDN       = 1 << 3
)

var (
	errTLShort       = errors.New("tl: buffer too short")
	errTLString      = errors.New("tl: malformed string")
	errTLCtor        = errors.New("tl: unexpected constructor")
	errTLBadDCOption = errors.New("tl: malformed dc_option")
)

// cursor reads fields from a TL binary buffer.
type cursor struct {
	data []byte
	off  int
}

func (c *cursor) u32() (uint32, error) {
	if c.off+4 > len(c.data) {
		return 0, errTLShort
	}
	v := binary.LittleEndian.Uint32(c.data[c.off:])
	c.off += 4
	return v, nil
}

// str decodes a TL string (1-byte length, padded to 4 bytes).
func (c *cursor) str() (string, error) {
	if c.off >= len(c.data) {
		return "", errTLShort
	}
	l := int(c.data[c.off])
	if l >= 0xfe {
		return "", errTLString
	}
	if l < 0 || c.off+1+l > len(c.data) {
		return "", errTLString
	}
	s := string(c.data[c.off+1 : c.off+1+l])
	c.off += 1 + l
	for c.off%4 != 0 {
		c.off++
	}
	return s, nil
}

func (c *cursor) skip(n int) error {
	if n < 0 || c.off+n > len(c.data) {
		return errTLShort
	}
	c.off += n
	return nil
}

func (c *cursor) remaining() []byte { return c.data[c.off:] }

func appendU32(dst []byte, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return append(dst, b[:]...)
}

// appendTLString appends s to dst in TL format (padded to 4 bytes). Lengths
// below 254 use the 1-byte header; longer strings use 0xfe + 4-byte LE length.
func appendTLString(dst []byte, s string) []byte {
	if len(s) < 0xfe {
		dst = append(dst, byte(len(s)))
		dst = append(dst, s...)
		for len(dst)%4 != 0 {
			dst = append(dst, 0)
		}
		return dst
	}
	dst = append(dst, 0xfe, byte(len(s)), byte(len(s)>>8), byte(len(s)>>16), byte(len(s)>>24))
	dst = append(dst, s...)
	for len(dst)%4 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// DC describes how Telegram DC endpoints are rewritten so the browser's
// MTProto connections land on the gateway's DC bridge:
//
//   - Domain mode (Domain set): every DC gets its own hostname dc<N>.<Domain>
//     on port 443. The public entrypoint (VPS nginx) routes the connection by
//     TLS SNI to the matching DC bridge, so clients behind firewalls that only
//     allow 80/443 can reach the bridge.
//   - Legacy mode (IP set): every DC points at IP on port PortBase+dc_id,
//     used for LAN setups where the bridge ports are reachable directly.
type DC struct {
	SameOrigin bool
	// Domain is the public domain for per-DC hostnames (dc1..dc5.<Domain>).
	Domain string
	// IP is the single address all DCs point at (legacy mode).
	IP string
	// PortBase is the base port for DC-specific ports (legacy mode).
	PortBase int
}

// Enabled reports whether the rewrite is configured at all.
func (d DC) Enabled() bool { return d.SameOrigin || d.Domain != "" || d.IP != "" }

// host returns the rewritten host for a DC id.
func (d DC) host(dc int32) string {
	if d.Domain != "" {
		return fmt.Sprintf("dc%d.%s", dc, d.Domain)
	}
	return d.IP
}

// port returns the rewritten port for a DC id.
func (d DC) port(dc int32) int {
	if d.Domain != "" {
		return 443
	}
	return d.PortBase + int(dc)
}

// dcOption is one entry of the config#330b4067.dc_options vector.
type dcOption struct {
	ID    int32
	Flags uint32
	IP    string
	Port  int32
	// Secret holds the raw TL-encoded bytes of the flags.10 secret field
	// (nil when the flag is unset), for byte-exact re-serialization.
	Secret []byte
}

// parse parses a single dcOption#5d8c6cc element; keeps the secret bytes as
// an opaque slice.
func (o *dcOption) parse(c *cursor) error {
	ctor, err := c.u32()
	if err != nil {
		return err
	}
	if ctor != dcOptionCtorLE {
		return fmt.Errorf("%w: got %#x", errTLCtor, ctor)
	}
	o.Flags, err = c.u32()
	if err != nil {
		return err
	}
	id, err := c.u32()
	if err != nil {
		return err
	}
	o.ID = int32(id)
	o.IP, err = c.str()
	if err != nil {
		return err
	}
	port, err := c.u32()
	if err != nil {
		return err
	}
	o.Port = int32(port)
	if o.Flags&dcOptionSecretBit != 0 {
		start := c.off
		if _, err := c.str(); err != nil {
			return err
		}
		o.Secret = dup(c.data[start:c.off])
	}
	return nil
}

// appendWith re-serializes the dcOption with the given ip and port (used for
// both the untouched pass-through and the rewritten variants).
func (o *dcOption) appendWith(dst []byte, ip string, port int32) []byte {
	dst = appendU32(dst, dcOptionCtorLE)
	dst = appendU32(dst, o.Flags)
	dst = appendU32(dst, uint32(o.ID))
	dst = appendTLString(dst, ip)
	dst = appendU32(dst, uint32(port))
	if o.Secret != nil {
		dst = append(dst, o.Secret...)
	}
	return dst
}

// appendRaw re-serializes the dcOption byte-identically (CDN/media-only/IPv6
// nodes are not tunnelled through the DC bridge).
func (o *dcOption) appendRaw(dst []byte) []byte {
	return o.appendWith(dst, o.IP, o.Port)
}

// append re-serializes the dcOption with the rewritten host/port from rw.
func (o *dcOption) append(dst []byte, rw DC) []byte {
	return o.appendWith(dst, rw.host(o.ID), int32(rw.port(o.ID)))
}

func dup(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// IsConfigTL reports whether buf starts with the config#330b4067 constructor.
func IsConfigTL(buf []byte) bool {
	return len(buf) >= 4 && binary.LittleEndian.Uint32(buf[:4]) == configCtorLE
}

// RewriteConfigTL rewrites every dc_option of a serialized config TL payload:
// in legacy mode ip_address becomes rewriteIP and the port becomes
// portBase+dc_id. Everything before the config object and trailing payload
// (auth padding, ...) is preserved byte-for-byte: only the dc_options block
// is re-serialized, so both size changes and surrounding byte layout are
// handled.
//
// The config may carry a 4-byte content_length_prefix before the TL object
// (MTProto HTTP transport); such a prefix is updated to the new inner size.
//
// Returns ok=false (no error) when the payload is not a well-formed config;
// callers then pass the body through untouched.
func RewriteConfigTL(body []byte, rewriteIP string, portBase int) (out []byte, ok bool, err error) {
	return rewriteConfigTL(body, DC{IP: rewriteIP, PortBase: portBase})
}

// rewriteConfigTL is the DC-aware variant used by the proxy: in domain mode
// (rw.Domain set) every dc_option points at dc<id>.<Domain> on port 443; in
// legacy mode at rw.IP on port rw.PortBase+dc_id.
func rewriteConfigTL(body []byte, rw DC) (out []byte, ok bool, err error) {
	if !rw.Enabled() {
		return body, false, nil
	}
	offset := 0
	switch {
	case IsConfigTL(body):
		// plain TL object
	case len(body) >= 8 && IsConfigTL(body[4:]):
		// 4-byte content_length_prefix before the config object (MTProto
		// HTTP transport). The prefix stores the TL object size; trailing
		// bytes (padding) are allowed after it.
		offset = 4
	default:
		return body, false, nil
	}

	c := &cursor{data: body, off: offset}
	if _, err := c.u32(); err != nil {
		return nil, false, err
	}
	// flags
	if err := c.skip(4); err != nil {
		return nil, false, err
	}
	// this_dc:int
	if err := c.skip(4); err != nil {
		return nil, false, err
	}
	// dc_options: Vector<dcOption> — wire format is just count:int.
	count, err := c.u32()
	if err != nil {
		return nil, false, err
	}
	if count > 10000 {
		return nil, false, fmt.Errorf("%w: absurd count %d", errTLBadDCOption, count)
	}
	opts := make([]dcOption, 0, count)
	for i := 0; i < int(count); i++ {
		var o dcOption
		if err := o.parse(c); err != nil {
			return nil, false, err
		}
		opts = append(opts, o)
	}
	rest := c.remaining()

	dst := make([]byte, 0, len(body))
	dst = append(dst, body[:offset]...)
	dst = appendU32(dst, configCtorLE)
	dst = appendU32(dst, binary.LittleEndian.Uint32(body[offset+4:offset+8]))  // flags
	dst = appendU32(dst, binary.LittleEndian.Uint32(body[offset+8:offset+12])) // this_dc
	dst = appendU32(dst, count)
	for i := range opts {
		o := &opts[i]
		if o.Flags&(dcOptionIPv6|dcOptionMediaOnly|dcOptionCDN) != 0 {
			// CDN/media-only/IPv6 nodes are not tunnelled through the DC
			// bridge: leave them byte-identical.
			dst = o.appendRaw(dst)
			continue
		}
		dst = o.append(dst, rw)
	}
	dst = append(dst, rest...)
	if offset == 4 {
		binary.LittleEndian.PutUint32(dst[:4], uint32(len(dst)-4))
	}
	return dst, true, nil
}

// isTLPayload reports whether the response could carry a serialized config TL
// object: binary-ish content, no content encoding (gzip would hide the TL
// constructor) and a body size small enough to buffer (config responses are
// a few KB; anything larger is media).
func isTLPayload(resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case ct == "", ct == "application/octet-stream", strings.HasPrefix(ct, "application/x-"):
		// MTProto responses typically omit content-type or use a binary type.
	default:
		return false
	}
	return resp.ContentLength <= maxConfigBody
}

const maxConfigBody = 1 << 20 // 1 MiB

// rewriteConfigBody buffers the response body, rewrites any config TL object
// inside it and re-wraps the result. It returns rewritten=true only when the
// payload was a config whose dc_options were replaced; the caller then fixes
// Content-Length. Non-config payloads pass through untouched with their
// original bytes, so streaming media is unaffected.
func rewriteConfigBody(r io.ReadCloser, rw DC) (body io.ReadCloser, rewritten bool, n int64, err error) {
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBody+1))
	if err != nil {
		return nil, false, 0, err
	}
	_ = r.Close()
	if len(data) > maxConfigBody {
		return io.NopCloser(bytes.NewReader(data)), false, int64(len(data)), nil
	}
	out, ok, err := rewriteConfigTL(data, rw)
	if err != nil {
		return io.NopCloser(bytes.NewReader(data)), false, int64(len(data)), err
	}
	if !ok {
		return io.NopCloser(bytes.NewReader(data)), false, int64(len(data)), nil
	}
	return io.NopCloser(bytes.NewReader(out)), true, int64(len(out)), nil
}
