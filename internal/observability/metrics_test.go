package observability_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/jesus-mata/tanugate/internal/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

func newTestCollector(t *testing.T) (*observability.MetricsCollector, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := observability.NewMetricsCollector(reg)
	return m, reg
}

func TestMetrics_RequestCounter(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	val := counterValue(t, m.RequestsTotal, "unknown", "GET", "200")
	if val != 3 {
		t.Errorf("expected counter=3, got %v", val)
	}
}

func TestMetrics_DurationHistogram(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	sum := histogramSum(t, m.RequestDuration, "unknown", "GET")
	if sum < 0.05 {
		t.Errorf("expected duration sum >= 0.05, got %v", sum)
	}
}

func TestMetrics_RequestSizeRecorded(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := strings.NewReader("hello world")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.ContentLength = int64(body.Len())
	handler.ServeHTTP(httptest.NewRecorder(), req)

	count := histogramCount(t, m.RequestSize, "unknown")
	if count != 1 {
		t.Errorf("expected 1 request size observation, got %d", count)
	}
}

func TestMetrics_ResponseSizeRecorded(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response body"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	count := histogramCount(t, m.ResponseSize, "unknown")
	if count != 1 {
		t.Errorf("expected 1 response size observation, got %d", count)
	}
}

func TestMetrics_UnknownContentLength(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("data"))
	req.ContentLength = -1
	handler.ServeHTTP(httptest.NewRecorder(), req)

	count := histogramCount(t, m.RequestSize, "unknown")
	if count != 0 {
		t.Errorf("expected 0 request size observations for unknown content length, got %d", count)
	}
}

func TestMetrics_RouteLabel(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	mr := &router.MatchedRoute{Config: &config.RouteConfig{Name: "my-route"}}
	req = req.WithContext(router.WithMatchedRoute(req.Context(), mr))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	val := counterValue(t, m.RequestsTotal, "my-route", "GET", "200")
	if val != 1 {
		t.Errorf("expected counter=1 for my-route, got %v", val)
	}
}

func TestMetrics_UnknownRouteLabel(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	val := counterValue(t, m.RequestsTotal, "unknown", "GET", "200")
	if val != 1 {
		t.Errorf("expected counter=1 for unknown route, got %v", val)
	}
}

func TestMetrics_StatusCodeLabel(t *testing.T) {
	m, _ := newTestCollector(t)
	mw := m.Middleware()

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	val := counterValue(t, m.RequestsTotal, "unknown", "POST", "201")
	if val != 1 {
		t.Errorf("expected counter=1 for status 201, got %v", val)
	}
}

func TestMetrics_Endpoint(t *testing.T) {
	m, reg := newTestCollector(t)

	// Make a request through the middleware to populate metrics.
	mw := m.Middleware()
	inner := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	inner.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/test", nil))

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(string(body), "gateway_requests_total") {
		t.Error("expected gateway_requests_total in /metrics output")
	}
}

func TestMetrics_AllDescriptorsRegistered(t *testing.T) {
	m, _ := newTestCollector(t)

	expected := map[string]bool{
		"gateway_requests_total":            false,
		"gateway_request_duration_seconds":  false,
		"gateway_request_size_bytes":        false,
		"gateway_response_size_bytes":       false,
		"gateway_circuit_breaker_state":     false,
		"gateway_rate_limit_rejected_total": false,
		"gateway_rate_limit_errors_total":   false,
		"gateway_upstream_errors_total":     false,
	}

	// Collect descriptors from each registered metric.
	collectors := []prometheus.Collector{
		m.RequestsTotal,
		m.RequestDuration,
		m.RequestSize,
		m.ResponseSize,
		m.CircuitBreakerState,
		m.RateLimitRejected,
		m.RateLimitErrors,
		m.UpstreamErrors,
	}

	for _, c := range collectors {
		ch := make(chan *prometheus.Desc, 10)
		c.Describe(ch)
		close(ch)
		for desc := range ch {
			name := desc.String()
			for ename := range expected {
				if strings.Contains(name, ename) {
					expected[ename] = true
				}
			}
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("metric %q not registered", name)
		}
	}
}

// helpers

func counterValue(t *testing.T, cv *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	var m dto.Metric
	if err := cv.WithLabelValues(labels...).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

func histogramSum(t *testing.T, hv *prometheus.HistogramVec, labels ...string) float64 {
	t.Helper()
	observer := hv.WithLabelValues(labels...)
	var m dto.Metric
	if err := observer.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetHistogram().GetSampleSum()
}

func histogramCount(t *testing.T, hv *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	observer := hv.WithLabelValues(labels...)
	var m dto.Metric
	if err := observer.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}
