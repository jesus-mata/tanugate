package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

// recorded holds the details captured by a test upstream server.
type recorded struct {
	mu      sync.Mutex
	method  string
	path    string
	headers http.Header
	host    string
}

func (r *recorded) get() (string, string, http.Header, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.method, r.path, r.headers, r.host
}

// newUpstream creates a test server that records incoming request details
// and replies with 200 OK.
func newUpstream(rec *recorded) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.headers = r.Header.Clone()
		rec.host = r.Host
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
}

func TestProxy_ForwardsRequest(t *testing.T) {
	rec := &recorded{}
	upstream := newUpstream(rec)
	defer upstream.Close()

	routeCfg := &config.RouteConfig{
		Name: "test-forward",
		Upstream: config.UpstreamConfig{
			URL:     upstream.URL,
			Timeout: 5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	req.Header.Set("X-Custom-Header", "test-value")
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	method, path, headers, _ := rec.get()

	if method != http.MethodGet {
		t.Errorf("expected method GET, got %s", method)
	}
	if path != "/some/path" {
		t.Errorf("expected path /some/path, got %s", path)
	}
	if got := headers.Get("X-Custom-Header"); got != "test-value" {
		t.Errorf("expected X-Custom-Header=test-value, got %s", got)
	}
}

func TestProxy_PathRewrite(t *testing.T) {
	rec := &recorded{}
	upstream := newUpstream(rec)
	defer upstream.Close()

	routeCfg := &config.RouteConfig{
		Name: "test-rewrite",
		Upstream: config.UpstreamConfig{
			URL:         upstream.URL,
			PathRewrite: "/internal/users/{id}",
			Timeout:     5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{"id": "42"},
	}

	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	_, path, _, _ := rec.get()

	if path != "/internal/users/42" {
		t.Errorf("expected path /internal/users/42, got %s", path)
	}
}

func TestProxy_PathRewriteMultipleParams(t *testing.T) {
	rec := &recorded{}
	upstream := newUpstream(rec)
	defer upstream.Close()

	routeCfg := &config.RouteConfig{
		Name: "test-rewrite-multi",
		Upstream: config.UpstreamConfig{
			URL:         upstream.URL,
			PathRewrite: "/api/{resource}/{id}",
			Timeout:     5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config: routeCfg,
		PathParams: map[string]string{
			"resource": "orders",
			"id":       "123",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	_, path, _, _ := rec.get()

	if path != "/api/orders/123" {
		t.Errorf("expected path /api/orders/123, got %s", path)
	}
}

func TestProxy_NoPathRewrite(t *testing.T) {
	rec := &recorded{}
	upstream := newUpstream(rec)
	defer upstream.Close()

	routeCfg := &config.RouteConfig{
		Name: "test-no-rewrite",
		Upstream: config.UpstreamConfig{
			URL:     upstream.URL,
			Timeout: 5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest(http.MethodGet, "/original/path", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	_, path, _, _ := rec.get()

	if path != "/original/path" {
		t.Errorf("expected path /original/path, got %s", path)
	}
}

func TestProxy_UpstreamDown_502(t *testing.T) {
	routeCfg := &config.RouteConfig{
		Name: "test-upstream-down",
		Upstream: config.UpstreamConfig{
			URL:     "http://127.0.0.1:1",
			Timeout: 5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", rr.Code)
	}
}

func TestProxy_ErrorResponseFormat(t *testing.T) {
	routeCfg := &config.RouteConfig{
		Name: "test-error-format",
		Upstream: config.UpstreamConfig{
			URL:     "http://127.0.0.1:1",
			Timeout: 5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body["error"] != "bad_gateway" {
		t.Errorf("expected error=bad_gateway, got %s", body["error"])
	}
}

func TestProxy_HostHeaderRewrite(t *testing.T) {
	rec := &recorded{}
	upstream := newUpstream(rec)
	defer upstream.Close()

	routeCfg := &config.RouteConfig{
		Name: "test-host-rewrite",
		Upstream: config.UpstreamConfig{
			URL:     upstream.URL,
			Timeout: 5 * time.Second,
		},
	}

	handler := NewProxyHandler(routeCfg)

	mr := &router.MatchedRoute{
		Config:     routeCfg,
		PathParams: map[string]string{},
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(req.Context(), mr)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	_, _, _, host := rec.get()

	// The upstream test server URL has the form "127.0.0.1:<port>".
	// The proxy Director sets req.Host to upstream.Host, so the upstream
	// should see its own host, not the original client host.
	wantHost := upstream.Listener.Addr().String()
	if host != wantHost {
		t.Errorf("expected Host header %s, got %s", wantHost, host)
	}
}
