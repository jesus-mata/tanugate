package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

func newTestLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

func TestLogging_BasicFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 log lines, got %d", len(lines))
	}

	completed := lines[1]
	for _, field := range []string{"method", "path", "status", "latency_ms", "request_id", "route", "remote_addr", "response_size"} {
		if _, ok := completed[field]; !ok {
			t.Errorf("missing field %q in completed log", field)
		}
	}
}

func TestLogging_RouteField(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	mr := &router.MatchedRoute{Config: &config.RouteConfig{Name: "test-route"}}
	req = req.WithContext(router.WithMatchedRoute(req.Context(), mr))

	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	if completed["route"] != "test-route" {
		t.Errorf("expected route=test-route, got %v", completed["route"])
	}
}

func TestLogging_UnknownRoute(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	if completed["route"] != "unknown" {
		t.Errorf("expected route=unknown, got %v", completed["route"])
	}
}

func TestLogging_StatusCodeCapture(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	if completed["status"].(float64) != 404 {
		t.Errorf("expected status=404, got %v", completed["status"])
	}
}

func TestLogging_DebugHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelDebug)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "value")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Test", "hello")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	foundReqHeaders := false
	foundRespHeaders := false
	for _, line := range lines {
		if line["msg"] == "request headers" {
			foundReqHeaders = true
		}
		if line["msg"] == "response headers" {
			foundRespHeaders = true
		}
	}
	if !foundReqHeaders {
		t.Error("expected request headers log at debug level")
	}
	if !foundRespHeaders {
		t.Error("expected response headers log at debug level")
	}
}

func TestLogging_InfoNoHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	for _, line := range lines {
		if line["msg"] == "request headers" || line["msg"] == "response headers" {
			t.Error("headers should not be logged at info level")
		}
	}
}

func TestLogging_LatencyIsPositive(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	latency := completed["latency_ms"].(float64)
	if latency < 0 {
		t.Errorf("expected latency >= 0, got %v", latency)
	}
}

func TestLogging_ImplicitStatusOK(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	if completed["status"].(float64) != 200 {
		t.Errorf("expected implicit status=200, got %v", completed["status"])
	}
}

func TestLogging_ResponseSize(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo)

	body := []byte("hello world")
	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := splitLogLines(buf.Bytes())
	completed := lines[1]
	if int(completed["response_size"].(float64)) != len(body) {
		t.Errorf("expected response_size=%d, got %v", len(body), completed["response_size"])
	}
}

func splitLogLines(data []byte) []map[string]any {
	var lines []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err == nil {
			lines = append(lines, m)
		}
	}
	return lines
}
