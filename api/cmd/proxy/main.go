package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/company/telegram-gateway/api/internal/database"
	"github.com/company/telegram-gateway/api/internal/db"
	"github.com/company/telegram-gateway/api/internal/proxy"
	"github.com/company/telegram-gateway/api/internal/session"
	"github.com/company/telegram-gateway/api/internal/turnrelay"
)

func main() {
	dsn := getenv("POSTGRES_DSN", "postgres://tg:tg_dev_password@localhost:5432/telegram_gateway")
	redisAddr := getenv("REDIS_ADDR", "localhost:6379")
	addr := getenv("PROXY_ADDR", ":8081")
	socks5 := getenv("SOCKS5_ADDR", "")

	ctx := context.Background()

	pool, err := database.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	rdb, err := database.NewRedis(ctx, redisAddr)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	// Outbound path to Telegram Web (ТЗ §10). When SOCKS5_ADDR is set, every
	// upstream request and backend health probe is tunnelled through it, so the
	// gateway needs no direct route to web.telegram.org.
	dial := proxy.NewDialContext(socks5)
	if socks5 != "" {
		if dial == nil {
			log.Fatalf("telegram reverse proxy: invalid SOCKS5_ADDR %q", socks5)
		}
		log.Printf("telegram reverse proxy: egress via SOCKS5 %s", socks5)
	}

	proxyPool := &proxy.Pool{Queries: db.New(pool), Dial: dial}

	// Background health checks keep proxy_pool current (auto-switching).
	go proxyPool.StartHealthChecks(ctx, 30*time.Second)

	// Unauthenticated raw-TCP transports are intentionally not started.
	// All browser DC connections use the session-gated same-origin route.

	h := proxy.NewHandler(db.New(pool), session.NewRedisStore(rdb), proxyPool, dial)
	h.SecureDC = true
	h.ConfigureRetry(parseInt(getenv("PROXY_RETRIES", "2"), proxy.DefaultRetries), proxy.DefaultRetryDelay)
	log.Printf("telegram reverse proxy: authenticated same-origin DC WebSocket enabled")

	h.TURN = startTURN(socks5)
	handler := proxy.ObserveHandler(h)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No Read/Write timeouts: media uploads/downloads and WebSocket
		// sessions are long-lived by nature.
	}

	// Prometheus metrics (S17 T17.2) on a dedicated port.
	metricsAddr := getenv("METRICS_ADDR", ":9092")
	metricsSrv := &http.Server{
		Addr:              metricsAddr,
		Handler:           proxy.MetricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("proxy metrics listening on %s", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("proxy metrics server: %v", err)
		}
	}()
	defer func() { _ = metricsSrv.Shutdown(ctx) }()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	log.Printf("telegram reverse proxy listening on %s", addr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

// startTURN runs the TURN relay for Telegram Web call media when TURN_ADDR
// is set (see package turnrelay) and returns what the proxy hands the
// browser; nil keeps calls on Telegram's own servers only.
func startTURN(socks5 string) *proxy.TURN {
	addr := getenv("TURN_ADDR", "")
	if addr == "" {
		return nil
	}
	secret := os.Getenv("TURN_SECRET")
	var urls []string
	for _, u := range strings.Split(getenv("TURN_URLS", ""), ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if secret == "" || len(urls) == 0 {
		log.Fatalf("TURN_ADDR is set but TURN_SECRET or TURN_URLS is empty")
	}
	var publicIP net.IP
	if v := getenv("TURN_PUBLIC_IP", ""); v != "" {
		if publicIP = net.ParseIP(v); publicIP == nil {
			log.Fatalf("invalid TURN_PUBLIC_IP %q", v)
		}
	}
	ttl, err := time.ParseDuration(getenv("TURN_CREDENTIAL_TTL", "12h"))
	if err != nil || ttl <= 0 {
		log.Fatalf("invalid TURN_CREDENTIAL_TTL: %v", err)
	}

	srv, err := turnrelay.Start(turnrelay.Config{
		ListenAddr: addr,
		Realm:      getenv("TURN_REALM", "telegram-gateway"),
		Secret:     secret,
		SocksAddr:  socks5,
		STUNServer: getenv("TURN_STUN_SERVER", "stun.cloudflare.com:3478"),
		PublicIP:   publicIP,
	})
	if err != nil {
		log.Fatalf("TURN relay: %v", err)
	}
	log.Printf("TURN relay listening on %s (tcp), media egress via SOCKS5 %s, ICE urls %v", srv.Addr(), socks5, urls)
	return &proxy.TURN{
		URLs:      urls,
		Secret:    secret,
		TTL:       ttl,
		RelayOnly: getenv("TURN_RELAY_ONLY", "") == "true",
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseInt(s string, fallback int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return fallback
}
