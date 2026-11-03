package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Telegram Web K's service worker (index.worker-<hash>.js) hardcodes the
// bootstrap MTProto endpoints of the real Telegram datacenters:
//
//	return `wss://${cfg.suffix.toLowerCase()}ws${dc}${kind}.web.telegram.org/${path}`
//
// The browser dials these hosts directly (wss://kws1.web.telegram.org/...)
// BEFORE any getConfig response arrives, so the dc_options rewrite
// (rewriteConfigBody) never runs when the client has no direct route to
// Telegram, and the DC bridge stays unused. To break the chicken-and-egg, the
// bundle's WebSocket URL constructor is rewritten so the very first
// connection lands on the gateway's DC bridge:
//
//	wss://${cfg.suffix.toLowerCase()}ws${dc}${kind}.web.telegram.org/${path}
//	→ domain mode: wss://dc${dc}.<Domain>/${path}          (port 443, SNI routing)
//	→ legacy mode: wss://<IP>:${<PortBase>+dc}/${path}
//
// In domain mode the public entrypoint (VPS nginx) selects the DC bridge by
// TLS SNI (dc1..dc5.<Domain>), so the whole DC path works over port 443 only
// — the one port guaranteed open on client networks. TLS terminates on the
// VPS (Let's Encrypt) and the plaintext WebSocket stream is tunneled through
// frp to the DC bridge, which re-encrypts to the real DC — the same path the
// rewritten dc_options use after getConfig.
//
// The pattern is matched structurally: only literals and the .suffix property
// name are pinned, the minified variable names are captured, so renames
// across Telegram releases don't break the rewrite. No match is fail-safe:
// the original body is served unchanged, the miss is logged and counted (see
// RewriteResult).
var wsBootstrapRE = regexp.MustCompile(
	`wss://\$\{[A-Za-z_$][A-Za-z0-9_$]*\.suffix\.toLowerCase\(\)\}` +
		`ws\$\{([A-Za-z_$][A-Za-z0-9_$]*)\}\$\{[A-Za-z_$][A-Za-z0-9_$]*\}\.web\.telegram\.org` +
		`/\$\{([A-Za-z_$][A-Za-z0-9_$]*)\}`,
)

const maxBundleBody = 8 << 20 // 8 MiB — worker bundles are ~1.5 MiB

// isBundleResponse reports whether the response is the Telegram Web worker
// bundle carrying the hardcoded DC bootstrap endpoints.
func isBundleResponse(resp *http.Response, urlPath string) bool {
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	base := path.Base(urlPath)
	if !strings.HasPrefix(base, "index.worker-") || !strings.HasSuffix(base, ".js") {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/javascript") && !strings.HasPrefix(ct, "text/javascript") {
		return false
	}
	return resp.ContentLength < 0 || resp.ContentLength <= maxBundleBody
}

// rewriteBundleBody buffers the response body and rewrites the WebSocket
// bootstrap URL constructor inside it. It returns rewritten=true only when a
// constructor was replaced; the caller then fixes Content-Length. The original
// body is always consumed and closed, mirroring rewriteConfigBody. A miss
// (Telegram changed the bundle shape) is logged and counted so the QR login
// never silently breaks again.
func rewriteBundleBody(r io.ReadCloser, rw DC) (body io.ReadCloser, rewritten bool, n int64, err error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBundleBody+1))
	if err != nil {
		return nil, false, 0, err
	}
	_ = r.Close()
	if len(data) > maxBundleBody {
		RewriteResult("bundle", false)
		return nil, false, 0, fmt.Errorf("telegram worker exceeds rewrite size limit")
	}
	out, ok, err := rewriteBootstrapBundle(data, rw)
	if err != nil {
		return io.NopCloser(bytes.NewReader(data)), false, int64(len(data)), err
	}
	if !ok {
		RewriteResult("bundle", false)
		log.Printf("proxy: bootstrap bundle: no DC WebSocket constructor found — Telegram changed the bundle shape, QR login will fail for clients without a direct route")
		if rw.SameOrigin {
			return nil, false, 0, fmt.Errorf("unsupported Telegram worker bundle")
		}
		return io.NopCloser(bytes.NewReader(data)), false, int64(len(data)), nil
	}
	RewriteResult("bundle", true)
	return io.NopCloser(bytes.NewReader(out)), true, int64(len(out)), nil
}

// rewriteBootstrapBundle rewrites every WebSocket bootstrap URL constructor in
// body to point at the DC bridge. In domain mode the host becomes
// dc${dc}.<Domain> on port 443 (SNI routing on the public entrypoint); in
// legacy mode the address is <IP> and the port is emitted as a template
// interpolation `:${<portBase>+dc}` so any DC_PORT_BASE works and the dc
// variable stays a runtime reference. Returns rewritten=false when no
// constructor was found (Telegram changed the bundle shape).
func rewriteBootstrapBundle(body []byte, rw DC) ([]byte, bool, error) {
	locs := wsBootstrapRE.FindAllSubmatchIndex(body, -1)
	if len(locs) == 0 {
		return body, false, nil
	}
	out := make([]byte, 0, len(body))
	last := 0
	for _, loc := range locs {
		out = append(out, body[last:loc[0]]...)
		dc := body[loc[2]:loc[3]] // group 1: dc variable (runtime reference)
		p := body[loc[4]:loc[5]]  // group 2: path variable (apiws token)
		if rw.SameOrigin {
			out = append(out, fmt.Sprintf("wss://${new URL(self.location.origin).host}/tg/_gw/dc/${%s}/${%s}", dc, p)...)
		} else if rw.Domain != "" {
			// wss://dc${dc}.<Domain>/${path} — port 443 (default), the VPS
			// routes the connection by SNI to the matching DC bridge.
			out = append(out, fmt.Sprintf("wss://dc${%s}.%s/${%s}", dc, rw.Domain, p)...)
		} else {
			port := strconv.Itoa(rw.PortBase)
			out = append(out, fmt.Sprintf("wss://%s:${%s+%s}/${%s}", rw.IP, port, dc, p)...)
		}
		last = loc[1]
	}
	out = append(out, body[last:]...)
	return out, true, nil
}
