package proxy

import (
	"context"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/company/telegram-gateway/api/internal/db"
)

// Queries is the subset of db.Querier the pool needs.
type Queries interface {
	ListActiveProxies(ctx context.Context) ([]db.ProxyPool, error)
	ListProxies(ctx context.Context) ([]db.ProxyPool, error)
	UpdateProxyHealth(ctx context.Context, arg db.UpdateProxyHealthParams) error
}

// Pool manages the proxy backends from proxy_pool (ТЗ §10). Proxies are used
// in priority order; failing ones are demoted to "inactive" and skipped until
// a background health check revives them.
type Pool struct {
	Queries      Queries
	CheckTimeout time.Duration
	// Dial optionally routes the TCP health probe through a custom dialer
	// (e.g. SOCKS5 via NewDialContext); nil dials directly.
	Dial DialContext
}

// DefaultCheckTimeout bounds every TCP health probe.
const DefaultCheckTimeout = 3 * time.Second

// Active returns the current usable proxy backends, newest/priority first.
func (p *Pool) Active(ctx context.Context) ([]db.ProxyPool, error) {
	return p.Queries.ListActiveProxies(ctx)
}

// Healthy reports whether the proxy backend is reachable over TCP. When the
// pool is configured with a Dial (SOCKS5), reachability is measured through
// the same tunnel the traffic uses.
func (p *Pool) Healthy(proxy db.ProxyPool) bool {
	u, err := url.Parse(proxy.Url)
	if err != nil {
		return false
	}
	host := u.Host
	if host == "" {
		return false
	}
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	timeout := p.CheckTimeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", host)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Mark marks a backend as active or inactive and refreshes last_checked_at.
func (p *Pool) Mark(ctx context.Context, id, status string) {
	if err := p.Queries.UpdateProxyHealth(ctx, db.UpdateProxyHealthParams{
		ID:     pgUUID(id),
		Status: status,
	}); err != nil {
		log.Printf("proxy pool: mark %s as %s: %v", id, status, err)
	}
}

// CheckOnce health-checks every backend (active and inactive) and refreshes
// their status: a reachable backend is promoted to "active", an unreachable
// one is demoted. Probing inactive rows is what revives a backend after an
// outage (ТЗ §10 "автопереключение и возврат").
func (p *Pool) CheckOnce(ctx context.Context) {
	proxies, err := p.Queries.ListProxies(ctx)
	if err != nil {
		log.Printf("proxy pool: list: %v", err)
		return
	}
	for _, pp := range proxies {
		healthy := p.Healthy(pp)
		BackendGauge(pp.Url, healthy)
		want := "inactive"
		if healthy {
			want = "active"
		}
		if pp.Status != want {
			p.Mark(ctx, pp.ID.String(), want)
		}
	}
}

// StartHealthChecks runs CheckOnce periodically until ctx is cancelled.
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.CheckOnce(ctx)
		}
	}
}

func pgUUID(s string) pgtype.UUID {
	var id pgtype.UUID
	_ = id.Scan(s)
	return id
}
