package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/router"
	"github.com/prometheus/client_golang/prometheus"
)

// MetricsCollector holds all Prometheus metrics for the gateway.
type MetricsCollector struct {
	RequestsTotal       *prometheus.CounterVec
	RequestDuration     *prometheus.HistogramVec
	RequestSize         *prometheus.HistogramVec
	ResponseSize        *prometheus.HistogramVec
	CircuitBreakerState *prometheus.GaugeVec
	RateLimitRejected   *prometheus.CounterVec
	RateLimitErrors     *prometheus.CounterVec
	UpstreamErrors      *prometheus.CounterVec
}

// NewMetricsCollector creates and registers all gateway metrics.
func NewMetricsCollector(reg prometheus.Registerer) *MetricsCollector {
	m := &MetricsCollector{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_requests_total",
				Help: "Total number of HTTP requests processed.",
			},
			[]string{"route", "method", "status_code"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_request_duration_seconds",
				Help:    "Duration of HTTP requests in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"route", "method"},
		),
		RequestSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_request_size_bytes",
				Help:    "Size of HTTP request bodies in bytes.",
				Buckets: prometheus.ExponentialBuckets(100, 10, 6),
			},
			[]string{"route"},
		),
		ResponseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_response_size_bytes",
				Help:    "Size of HTTP response bodies in bytes.",
				Buckets: prometheus.ExponentialBuckets(100, 10, 6),
			},
			[]string{"route"},
		),
		CircuitBreakerState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "gateway_circuit_breaker_state",
				Help: "Current state of circuit breakers (0=closed, 1=open, 2=half-open).",
			},
			[]string{"route", "state"},
		),
		RateLimitRejected: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_rate_limit_rejected_total",
				Help: "Total number of requests rejected by rate limiting.",
			},
			[]string{"route"},
		),
		RateLimitErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_rate_limit_errors_total",
				Help: "Total number of rate limiter backend errors (fail-open events).",
			},
			[]string{"route"},
		),
		UpstreamErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_upstream_errors_total",
				Help: "Total number of upstream errors.",
			},
			[]string{"route", "error_type"},
		),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.RequestSize,
		m.ResponseSize,
		m.CircuitBreakerState,
		m.RateLimitRejected,
		m.RateLimitErrors,
		m.UpstreamErrors,
	)

	return m
}

// Middleware returns a middleware.Middleware that records request metrics.
func (m *MetricsCollector) Middleware() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.WrapResponseWriter(w)

			next.ServeHTTP(ww, r)

			route := "unknown"
			if mr := router.RouteFromContext(r.Context()); mr != nil {
				route = mr.Config.Name
			}

			duration := time.Since(start).Seconds()
			status := strconv.Itoa(ww.StatusCode())

			m.RequestsTotal.WithLabelValues(route, r.Method, status).Inc()
			m.RequestDuration.WithLabelValues(route, r.Method).Observe(duration)

			if r.ContentLength >= 0 {
				m.RequestSize.WithLabelValues(route).Observe(float64(r.ContentLength))
			}

			m.ResponseSize.WithLabelValues(route).Observe(float64(ww.BytesWritten()))
		})
	}
}
