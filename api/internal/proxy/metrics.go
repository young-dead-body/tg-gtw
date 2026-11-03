package proxy

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Proxy-level Prometheus metrics (S17 T17.2) exposed by the Telegram reverse
// proxy on its metrics port.
var (
	registry = prometheus.NewRegistry()
	factory  = promauto.With(registry)

	dcFailures = factory.NewCounter(prometheus.CounterOpts{
		Name: "telegram_gateway_proxy_dc_failures_total", Help: "Failed DC WebSocket upstream connections.",
	})

	// backendActive tracks reachability of every configured proxy backend by
	// URL (1 = reachable, 0 = demoted). Updated by health checks.
	backendActive = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "telegram_gateway",
		Subsystem: "proxy",
		Name:      "backend_active",
		Help:      "Backend reachability by URL (1 active, 0 inactive).",
	}, []string{"url"})

	// httpRequests counts proxied HTTP requests by path length bucket-free;
	// method and status are recorded per request.
	httpRequests = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: "telegram_gateway",
		Subsystem: "proxy",
		Name:      "requests_total",
		Help:      "Total HTTP requests proxied.",
	}, []string{"method"})

	// requestDuration buckets proxy handler latency.
	requestDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "telegram_gateway",
		Subsystem: "proxy",
		Name:      "request_duration_seconds",
		Help:      "Proxy handler latency in seconds.",
		Buckets:   []float64{0.005, 0.025, 0.1, 0.5, 1, 2.5, 5},
	}, []string{})

	// rewriteTotal counts DC endpoint rewrites (bootstrap bundle and config
	// dc_options) by kind and result. A bundle miss means Telegram changed the
	// worker bundle shape and clients without a direct route cannot load the
	// login QR code anymore.
	rewriteTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: "telegram_gateway",
		Subsystem: "proxy",
		Name:      "rewrite_total",
		Help:      "Telegram DC endpoint rewrites by kind (bundle/config) and result (hit/miss).",
	}, []string{"kind", "result"})
)

func init() {
	registry.MustRegister(collectors.NewGoCollector())
}

// BackendGauge records the reachability of a proxy backend URL.
func BackendGauge(url string, active bool) {
	val := 0.0
	if active {
		val = 1
	}
	backendActive.WithLabelValues(url).Set(val)
}

// MetricsHandler serves prometheus metrics for the proxy service.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// RewriteResult records the outcome of a DC endpoint rewrite (bootstrap
// bundle or config dc_options). misses are the canary for Telegram changing
// the bundle/config shape without notice.
func RewriteResult(kind string, hit bool) {
	result := "hit"
	if !hit {
		result = "miss"
	}
	rewriteTotal.WithLabelValues(kind, result).Inc()
}

// ObserveHandler wraps a net/http handler and reports count/latency metrics.
func ObserveHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		httpRequests.WithLabelValues(r.Method).Inc()
		requestDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	})
}
