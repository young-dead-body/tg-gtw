package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics collects Prometheus gauges and HTTP request metrics for the API
// service (ТЗ §17, T17.2). The registry is isolated from the default one so
// the process is deterministic in tests.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	postgresUp   prometheus.Gauge
	redisUp      prometheus.Gauge
	proxyUp      prometheus.Gauge
	backupLast   prometheus.Gauge
	backupOK     prometheus.Gauge
	backupRun    prometheus.Gauge
}

// New builds a metrics registry and registers all collectors.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	m := &Metrics{
		registry:   reg,
		backupRun:  factory.NewGauge(prometheus.GaugeOpts{Name: "telegram_gateway_backup_last_run_timestamp_seconds", Help: "Timestamp of the last backup attempt."}),
		backupLast: factory.NewGauge(prometheus.GaugeOpts{Name: "telegram_gateway_backup_last_success_timestamp_seconds", Help: "Unix timestamp of the last successful database backup."}),
		backupOK:   factory.NewGauge(prometheus.GaugeOpts{Name: "telegram_gateway_backup_last_run_success", Help: "Whether the last backup run succeeded."}),
		httpRequests: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_gateway",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total HTTP requests served.",
		}, []string{"method", "route", "status"}),
		httpDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "telegram_gateway",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request latency by route in seconds.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"route"}),
		postgresUp: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "telegram_gateway",
			Subsystem: "dependencies",
			Name:      "postgres_up",
			Help:      "1 if PostgreSQL is reachable, 0 otherwise.",
		}),
		redisUp: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "telegram_gateway",
			Subsystem: "dependencies",
			Name:      "redis_up",
			Help:      "1 if Redis is reachable, 0 otherwise.",
		}),
		proxyUp: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "telegram_gateway",
			Subsystem: "dependencies",
			Name:      "telegram_proxy_up",
			Help:      "1 if the Telegram proxy backend is reachable, 0 otherwise.",
		}),
	}

	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Registry exposes the Prometheus registry.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns a net/http handler exposing the metrics in Prometheus text
// format. It is served on a dedicated metrics port.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// FiberMiddleware records per-route request count and latency.
func (m *Metrics) FiberMiddleware(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()

	route := c.Route().Path
	if route == "" {
		route = "unknown"
	}
	status := c.Response().StatusCode()
	if status == 0 {
		status = fiber.StatusOK
	}
	m.httpRequests.WithLabelValues(c.Method(), route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	return err
}

// SetPostgresUp updates the PostgreSQL reachability gauge.
func (m *Metrics) SetPostgresUp(ok bool) { set(m.postgresUp, ok) }

// SetRedisUp updates the Redis reachability gauge.
func (m *Metrics) SetRedisUp(ok bool) { set(m.redisUp, ok) }

// SetProxyUp updates the Telegram proxy reachability gauge.
func (m *Metrics) SetProxyUp(ok bool) { set(m.proxyUp, ok) }

func set(g prometheus.Gauge, ok bool) {
	if ok {
		g.Set(1)
	} else {
		g.Set(0)
	}
}

func (m *Metrics) RecordBackup(at time.Time, ok bool) {
	m.backupRun.Set(float64(time.Now().Unix()))
	set(m.backupOK, ok)
	if ok {
		m.backupLast.Set(float64(at.Unix()))
	}
}
