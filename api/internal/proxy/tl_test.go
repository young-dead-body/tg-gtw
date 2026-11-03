package proxy

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildConfigTL constructs a config#330b4067 payload around a given dc_options
// byte block, following ctor + flags + this_dc + dc_options layout.
func buildConfigTL(dcOptions []byte) []byte {
	var b []byte
	b = appendU32(b, configCtorLE)
	b = appendU32(b, 0) // flags
	b = appendU32(b, 2) // this_dc
	b = append(b, dcOptions...)
	return b
}

// buildDCOption encodes a dcOption element.
func buildDCOption(id int32, flags uint32, ip string, port int32, secret []byte) []byte {
	var b []byte
	b = appendU32(b, dcOptionCtorLE)
	b = appendU32(b, flags)
	b = appendU32(b, uint32(id))
	b = appendTLString(b, ip)
	b = appendU32(b, uint32(port))
	if secret != nil {
		b = appendTLString(b, string(secret))
	}
	return b
}

// encodeCount produces the dc_options vector wire block: count + entries.
func encodeCount(entries ...[]byte) []byte {
	var b []byte
	b = appendU32(b, uint32(len(entries)))
	for _, e := range entries {
		b = append(b, e...)
	}
	return b
}

// parseConfigTL round-trips a config body to extract the dc_options.
type parsedDCOption struct {
	ID     int32
	Flags  uint32
	IP     string
	Port   int32
	Secret []byte
}

func parseDCOptions(t *testing.T, body []byte) []parsedDCOption {
	t.Helper()
	require.True(t, IsConfigTL(body), "body must start with config ctor")
	c := &cursor{data: body}
	ctor, err := c.u32()
	require.NoError(t, err)
	require.Equal(t, uint32(configCtorLE), ctor)
	for range 2 {
		_, err = c.u32() // flags, this_dc
		require.NoError(t, err)
	}
	count, err := c.u32()
	require.NoError(t, err)
	out := make([]parsedDCOption, 0, count)
	for i := 0; i < int(count); i++ {
		var o dcOption
		require.NoError(t, o.parse(c))
		out = append(out, parsedDCOption(o))
	}
	return out
}

func TestRewriteConfigTLRewritesDCOptions(t *testing.T) {
	body := buildConfigTL(encodeCount(
		buildDCOption(1, 0, "149.154.175.50", 443, nil),
		buildDCOption(2, dcOptionSecretBit, "149.154.167.51", 443, []byte("secret12345")),
		buildDCOption(3, 0, "149.154.175.100", 443, nil),
		buildDCOption(4, 0, "91.108.56.130", 443, nil),
		buildDCOption(5, 0, "149.154.167.91", 443, nil),
	))

	out, ok, err := RewriteConfigTL(body, "192.0.2.10", 20000)
	require.NoError(t, err)
	require.True(t, ok)

	dcs := parseDCOptions(t, out)
	require.Len(t, dcs, 5)
	for i, want := range []struct {
		id   int32
		port int32
		ip   string
	}{
		{1, 20001, "192.0.2.10"},
		{2, 20002, "192.0.2.10"},
		{3, 20003, "192.0.2.10"},
		{4, 20004, "192.0.2.10"},
		{5, 20005, "192.0.2.10"},
	} {
		require.Equal(t, want.id, dcs[i].ID)
		require.Equal(t, want.port, dcs[i].Port, "dc %d port", want.id)
		require.Equal(t, want.ip, dcs[i].IP)
	}
	// DC 2 keeps its secret bytes verbatim (TL-encoded, i.e. prefixed length).
	require.Equal(t, append([]byte{0x0b}, "secret12345"...), dcs[1].Secret)
}

func TestRewriteConfigTLDomainMode(t *testing.T) {
	body := buildConfigTL(encodeCount(
		buildDCOption(1, 0, "149.154.175.50", 443, nil),
		buildDCOption(2, dcOptionSecretBit, "149.154.167.51", 443, []byte("secret12345")),
		buildDCOption(3, dcOptionCDN, "149.154.175.100", 443, nil),
		buildDCOption(4, 0, "91.108.56.130", 443, nil),
		buildDCOption(5, 0, "149.154.167.91", 443, nil),
	))

	out, ok, err := rewriteConfigTL(body, DC{Domain: "gateway.example.com"})
	require.NoError(t, err)
	require.True(t, ok)

	dcs := parseDCOptions(t, out)
	require.Len(t, dcs, 5)
	for i, want := range []struct {
		id   int32
		port int32
		ip   string
	}{
		{1, 443, "dc1.gateway.example.com"},
		{2, 443, "dc2.gateway.example.com"},
		{3, 443, "149.154.175.100"}, // CDN entry stays untouched
		{4, 443, "dc4.gateway.example.com"},
		{5, 443, "dc5.gateway.example.com"},
	} {
		require.Equal(t, want.id, dcs[i].ID)
		require.Equal(t, want.port, dcs[i].Port, "dc %d port", want.id)
		require.Equal(t, want.ip, dcs[i].IP)
	}
	// DC 2 keeps its secret bytes verbatim (TL-encoded, i.e. prefixed length).
	require.Equal(t, append([]byte{0x0b}, "secret12345"...), dcs[1].Secret)
}

func TestRewriteConfigTLDisabled(t *testing.T) {
	body := buildConfigTL(encodeCount(buildDCOption(1, 0, "149.154.175.50", 443, nil)))
	out, ok, err := rewriteConfigTL(body, DC{})
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, body, out)
}

func TestRewriteKeepsCDNAndIPv6AndUnknownFlagsUntouched(t *testing.T) {
	body := buildConfigTL(encodeCount(
		buildDCOption(2, dcOptionCDN, "149.154.175.1", 443, nil),
		buildDCOption(3, dcOptionMediaOnly, "149.154.175.2", 443, nil),
		buildDCOption(4, dcOptionIPv6, "2001:db8::1", 443, nil),
		buildDCOption(5, 0, "149.154.175.3", 443, nil),
	))

	out, ok, err := RewriteConfigTL(body, "192.0.2.10", 20000)
	require.NoError(t, err)
	require.True(t, ok)

	opts := parseDCOptions(t, out)
	require.Len(t, opts, 4)
	require.Equal(t, "149.154.175.1", opts[0].IP)
	require.Equal(t, int32(443), opts[0].Port)
	require.Equal(t, "149.154.175.2", opts[1].IP)
	require.Equal(t, "2001:db8::1", opts[2].IP)
	require.Equal(t, "192.0.2.10", opts[3].IP)
	require.Equal(t, int32(20005), opts[3].Port)
}

func TestRewriteConfigNotConfig(t *testing.T) {
	body := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	out, ok, err := RewriteConfigTL(body, "192.0.2.10", 20000)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, body, out)
}

func TestSurroundingBytesPreserved(t *testing.T) {
	// MTProto HTTP transport prepends a 4-byte content length prefix (total
	// content length after it); the dc_options block must be rewritten while
	// the prefix is updated and anything else stays byte-identical.
	inner := buildConfigTL(encodeCount(buildDCOption(1, 0, "149.154.175.50", 443, nil)))
	body := make([]byte, 4+len(inner)+2)
	binary.LittleEndian.PutUint32(body[:4], uint32(len(body)-4))
	copy(body[4:4+len(inner)], inner)
	body[len(body)-2] = 0xAA
	body[len(body)-1] = 0xBB

	out, ok, err := RewriteConfigTL(body, "192.0.2.10", 20000)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint32(len(out)-4), binary.LittleEndian.Uint32(out[:4]), "content length prefix updated")
	require.Equal(t, byte(0xAA), out[len(out)-2])
	require.Equal(t, byte(0xBB), out[len(out)-1])
	opts := parseDCOptions(t, out[4:])
	require.Len(t, opts, 1)
	require.Equal(t, "192.0.2.10", opts[0].IP)
	require.Equal(t, int32(20001), opts[0].Port)
}
