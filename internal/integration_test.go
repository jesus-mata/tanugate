package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/middleware/circuitbreaker"
	"github.com/jesus-mata/tanugate/internal/middleware/ratelimit"
	"github.com/jesus-mata/tanugate/internal/middleware/retry"
	"github.com/jesus-mata/tanugate/internal/middleware/transform"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/jesus-mata/tanugate/internal/proxy"
	"github.com/jesus-mata/tanugate/internal/router"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const testJWTSecret = "integration-test-secret-key-1234"
const testAPIKey = "test-api-key-xyz"

// mockUpstream echoes request details and can be configured to fail.
type mockUpstream struct {
	mu         sync.Mutex
	failCount  int
	requestLog []capturedRequest
}

type capturedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    string
}

func (m *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var bodyBytes []byte
	if r.Body != nil {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		bodyBytes = buf.Bytes()
	}

	m.mu.Lock()
	m.requestLog = append(m.requestLog, capturedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: r.Header.Clone(),
		Body:    string(bodyBytes),
	})

	if m.failCount > 0 {
		m.failCount--
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "service_unavailable"})
		return
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Upstream-Path", r.URL.Path)
	w.Header().Set("X-Powered-By", "mock-upstream")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"echo_path":   r.URL.Path,
		"echo_method": r.Method,
		"echo_body":   string(bodyBytes),
		"internal_id": "db-12345",
		"db_id":       "67890",
	})
}

func (m *mockUpstream) setFailCount(n int) {
	m.mu.Lock()
	m.failCount = n
	m.mu.Unlock()
}

func (m *mockUpstream) lastRequest() capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requestLog) == 0 {
		return capturedRequest{}
	}
	return m.requestLog[len(m.requestLog)-1]
}

// generateTestJWT creates a signed HS256 JWT token.
func generateTestJWT(secret string, claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		panic(fmt.Sprintf("failed to sign test JWT: %v", err))
	}
	return signed
}

func validJWT() string {
	return generateTestJWT(testJWTSecret, jwt.MapClaims{
		"sub": "user-123",
		"iss": "test-issuer",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
}

// testGateway holds all components for integration testing.
type testGateway struct {
	handler  http.Handler
	upstream *mockUpstream
	metrics  *observability.MetricsCollector
}

func newTestGateway(t *testing.T) *testGateway {
	t.Helper()

	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	routes := []config.RouteConfig{
		{
			Name: "test-public",
			Match: config.MatchConfig{
				PathRegex: `^/public/(?P<rest>.*)$`,
				Methods:   []string{"GET"},
			},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/api/{rest}",
				Timeout:     5 * time.Second,
			},
		},
		{
			Name: "test-jwt",
			Match: config.MatchConfig{
				PathRegex: `^/api/jwt/(?P<rest>.*)$`,
				Methods:   []string{"GET", "POST", "PUT", "DELETE"},
			},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/jwt/{rest}",
				Timeout:     5 * time.Second,
			},
			Auth: &config.RouteAuthConfig{Providers: []string{"jwt_default"}},
			RateLimit: &config.RouteLimitConfig{
				RequestsPerWindow: 5,
				Window:            60 * time.Second,
				KeySource:         "ip",
			},
			Transform: &config.TransformConfig{
				Request: &config.DirectionTransform{
					Headers: &config.HeaderTransform{
						Add:    map[string]string{"X-Gateway-Route": "${route_name}"},
						Remove: []string{"X-Internal-Debug"},
					},
				},
				Response: &config.DirectionTransform{
					Headers: &config.HeaderTransform{
						Add:    map[string]string{"X-Served-By": "api-gateway"},
						Remove: []string{"X-Powered-By"},
					},
				},
			},
		},
		{
			Name: "test-apikey",
			Match: config.MatchConfig{
				PathRegex: `^/api/key/(?P<rest>.*)$`,
				Methods:   []string{"GET", "POST"},
			},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/key/{rest}",
				Timeout:     5 * time.Second,
			},
			Auth: &config.RouteAuthConfig{Providers: []string{"apikey_svc1"}},
			Transform: &config.TransformConfig{
				Request: &config.DirectionTransform{
					Body: &config.BodyTransform{
						InjectFields: map[string]any{"_source": "api-gateway"},
						StripFields:  []string{"secret_field"},
					},
				},
				Response: &config.DirectionTransform{
					Body: &config.BodyTransform{
						StripFields: []string{"internal_id"},
						RenameKeys:  map[string]string{"db_id": "id"},
					},
				},
			},
		},
		{
			Name: "test-resilience",
			Match: config.MatchConfig{
				PathRegex: `^/api/resilience/(?P<rest>.*)$`,
			},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/resilience/{rest}",
				Timeout:     5 * time.Second,
			},
			Retry: &config.RetryConfig{
				MaxRetries:           2,
				InitialDelay:         1 * time.Millisecond,
				Multiplier:           1.0,
				RetryableStatusCodes: []int{503},
			},
			CircuitBreaker: &config.CircuitBreakerConfig{
				FailureThreshold: 3,
				SuccessThreshold: 1,
				Timeout:          100 * time.Millisecond,
			},
		},
		{
			Name: "test-cors-override",
			Match: config.MatchConfig{
				PathRegex: `^/api/cors/(?P<rest>.*)$`,
			},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/cors/{rest}",
				Timeout:     5 * time.Second,
			},
			CORS: &config.CORSConfig{
				AllowedOrigins:   []string{"https://override.example.com"},
				AllowedMethods:   []string{"GET"},
				AllowedHeaders:   []string{"Authorization"},
				AllowCredentials: true,
				MaxAge:           600,
			},
		},
	}

	authenticators := map[string]auth.Authenticator{
		"jwt_default": mustAuth(t, config.AuthProvider{
			Type: "jwt",
			JWT: &config.JWTConfig{
				Secret:    testJWTSecret,
				Algorithm: "HS256",
				Issuer:    "test-issuer",
			},
		}),
		"apikey_svc1": mustAuth(t, config.AuthProvider{
			Type: "apikey",
			APIKey: &config.APIKeyConfig{
				Header: "X-API-Key",
				Keys:   []config.APIKeyEntry{{Key: testAPIKey, Name: "test-client"}},
			},
		}),
	}

	registry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(registry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	handlers := make(map[string]http.Handler, len(routes))
	for i := range routes {
		h := proxy.NewProxyHandler(&routes[i])
		route := &routes[i]

		if route.CircuitBreaker != nil {
			cb := circuitbreaker.New(route.CircuitBreaker, route.Name,
				circuitbreaker.WithOnStateChange(func(routeName string, from, to circuitbreaker.State) {
					metrics.CircuitBreakerState.WithLabelValues(routeName, to.String()).Set(1)
					metrics.CircuitBreakerState.WithLabelValues(routeName, from.String()).Set(0)
				}),
			)
			if route.Retry != nil {
				h = retry.Retry(route.Retry, cb, h, retry.WithSleep(func(time.Duration) {}))
			} else {
				h = retry.WithCircuitBreaker(cb, h)
			}
		} else if route.Retry != nil {
			h = retry.Retry(route.Retry, nil, h, retry.WithSleep(func(time.Duration) {}))
		}

		if route.Transform != nil {
			h = transform.RequestTransform(route.Transform.Request, route.Transform.MaxBodySize)(
				transform.ResponseTransform(route.Transform.Response, route.Transform.MaxBodySize)(h))
		}

		if route.CORS != nil {
			h = middleware.CORSOverride(*route.CORS)(h)
		}

		// Auth and rate-limit are per-route (router sets context before dispatch).
		h = auth.Middleware(slog.Default(), authenticators)(h)
		h = ratelimit.RateLimit(limiter, metrics, nil)(h)

		handlers[route.Name] = h
	}

	r := router.New(routes, handlers)

	corsConfig := config.CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-API-Key"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	globalChain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
		metrics.Middleware(),
		middleware.CORS(corsConfig),
	)

	gwConfig := &config.GatewayConfig{
		RateLimit: config.RateLimitGlobalConfig{Backend: "memory"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", observability.HealthHandler(gwConfig, nil))
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("/", globalChain(r))

	return &testGateway{
		handler:  mux,
		upstream: mock,
		metrics:  metrics,
	}
}

func mustAuth(t *testing.T, provider config.AuthProvider) auth.Authenticator {
	t.Helper()
	a, err := auth.NewAuthenticator(provider)
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}
	return a
}

func doRequest(handler http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	return rr
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	return body
}

// --- Test Cases (26 tests) ---

func TestIntegration_RequestForwarding(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/public/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["echo_path"] != "/api/items" {
		t.Errorf("expected path /api/items, got %v", body["echo_path"])
	}
}

func TestIntegration_AuthJWTValid(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/users", map[string]string{
		"Authorization": "Bearer " + validJWT(),
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestIntegration_AuthJWTInvalid(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/users", map[string]string{
		"Authorization": "Bearer invalid-token",
	}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["error"] != "unauthorized" {
		t.Errorf("expected error=unauthorized, got %v", body["error"])
	}
}

func TestIntegration_AuthJWTMissing(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/users", nil, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestIntegration_AuthJWTExpired(t *testing.T) {
	gw := newTestGateway(t)
	expired := generateTestJWT(testJWTSecret, jwt.MapClaims{
		"sub": "user-123",
		"iss": "test-issuer",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	rr := doRequest(gw.handler, "GET", "/api/jwt/users", map[string]string{
		"Authorization": "Bearer " + expired,
	}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	msg, _ := body["message"].(string)
	if msg != "authentication failed" {
		t.Errorf("expected 'authentication failed' message, got %q", msg)
	}
}

func TestIntegration_AuthAPIKeyValid(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/key/orders", map[string]string{
		"X-API-Key": testAPIKey,
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestIntegration_AuthAPIKeyInvalid(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/key/orders", map[string]string{
		"X-API-Key": "wrong-key",
	}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestIntegration_AuthPublicRoute(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/public/data", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestIntegration_RateLimitEnforced(t *testing.T) {
	gw := newTestGateway(t)
	headers := map[string]string{"Authorization": "Bearer " + validJWT()}

	for i := 0; i < 5; i++ {
		rr := doRequest(gw.handler, "GET", "/api/jwt/rl-test", headers, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	rr := doRequest(gw.handler, "GET", "/api/jwt/rl-test", headers, "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["error"] != "rate_limit_exceeded" {
		t.Errorf("expected error=rate_limit_exceeded, got %v", body["error"])
	}
}

func TestIntegration_RateLimitHeaders(t *testing.T) {
	gw := newTestGateway(t)
	headers := map[string]string{"Authorization": "Bearer " + validJWT()}

	rr := doRequest(gw.handler, "GET", "/api/jwt/rl-headers", headers, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if rr.Header().Get(h) == "" {
			t.Errorf("missing %s header", h)
		}
	}
}

func TestIntegration_CircuitBreakerOpens(t *testing.T) {
	gw := newTestGateway(t)
	gw.upstream.setFailCount(100)

	for i := 0; i < 3; i++ {
		doRequest(gw.handler, "GET", "/api/resilience/cb-test", nil, "")
	}

	rr := doRequest(gw.handler, "GET", "/api/resilience/cb-test", nil, "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d; body: %s", rr.Code, rr.Body.String())
	}
	body := decodeJSON(t, rr)
	if body["error"] != "service_unavailable" {
		t.Errorf("expected error=service_unavailable, got %v", body["error"])
	}
}

func TestIntegration_CircuitBreakerRecovers(t *testing.T) {
	gw := newTestGateway(t)
	gw.upstream.setFailCount(100)

	for i := 0; i < 5; i++ {
		doRequest(gw.handler, "GET", "/api/resilience/cb-recover", nil, "")
	}

	time.Sleep(150 * time.Millisecond)
	gw.upstream.setFailCount(0)

	rr := doRequest(gw.handler, "GET", "/api/resilience/cb-recover", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after CB recovery, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestIntegration_RetryOnUpstreamFailure(t *testing.T) {
	gw := newTestGateway(t)
	gw.upstream.setFailCount(2)

	rr := doRequest(gw.handler, "GET", "/api/resilience/retry-test", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

func TestIntegration_HeaderTransformAdd(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/transform", map[string]string{
		"Authorization": "Bearer " + validJWT(),
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	last := gw.upstream.lastRequest()
	if last.Headers.Get("X-Gateway-Route") != "test-jwt" {
		t.Errorf("expected X-Gateway-Route=test-jwt, got %q", last.Headers.Get("X-Gateway-Route"))
	}
}

func TestIntegration_HeaderTransformRemove(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/transform-rm", map[string]string{
		"Authorization":    "Bearer " + validJWT(),
		"X-Internal-Debug": "true",
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	last := gw.upstream.lastRequest()
	if last.Headers.Get("X-Internal-Debug") != "" {
		t.Error("X-Internal-Debug should have been removed")
	}
}

func TestIntegration_ResponseTransformHeaders(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/jwt/resp-hdr", map[string]string{
		"Authorization": "Bearer " + validJWT(),
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("X-Served-By") != "api-gateway" {
		t.Errorf("expected X-Served-By=api-gateway, got %q", rr.Header().Get("X-Served-By"))
	}
	if rr.Header().Get("X-Powered-By") != "" {
		t.Error("X-Powered-By should have been removed from response")
	}
}

func TestIntegration_BodyTransformInject(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "POST", "/api/key/body-inject", map[string]string{
		"X-API-Key": testAPIKey,
	}, `{"name":"test"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	last := gw.upstream.lastRequest()
	var upBody map[string]any
	if err := json.Unmarshal([]byte(last.Body), &upBody); err != nil {
		t.Fatalf("failed to parse upstream body: %v", err)
	}
	if upBody["_source"] != "api-gateway" {
		t.Errorf("expected _source=api-gateway, got %v", upBody["_source"])
	}
}

func TestIntegration_BodyTransformStrip(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "POST", "/api/key/body-strip", map[string]string{
		"X-API-Key": testAPIKey,
	}, `{"name":"test","secret_field":"s3cret"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	last := gw.upstream.lastRequest()
	var upBody map[string]any
	if err := json.Unmarshal([]byte(last.Body), &upBody); err != nil {
		t.Fatalf("failed to parse upstream body: %v", err)
	}
	if _, ok := upBody["secret_field"]; ok {
		t.Error("secret_field should have been stripped")
	}
}

func TestIntegration_ResponseBodyTransformRename(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/api/key/body-rename", map[string]string{
		"X-API-Key": testAPIKey,
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if _, ok := body["internal_id"]; ok {
		t.Error("internal_id should have been stripped from response")
	}
	if body["id"] == nil {
		t.Error("db_id should have been renamed to id in response")
	}
}

func TestIntegration_CORSPreflightResponse(t *testing.T) {
	gw := newTestGateway(t)
	req := httptest.NewRequest("OPTIONS", "/public/data", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rr := httptest.NewRecorder()
	gw.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("expected origin https://example.com, got %q",
			rr.Header().Get("Access-Control-Allow-Origin"))
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("missing Access-Control-Allow-Methods")
	}
}

func TestIntegration_CORSRegularResponse(t *testing.T) {
	gw := newTestGateway(t)
	req := httptest.NewRequest("GET", "/public/data", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	gw.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("missing Access-Control-Allow-Origin on regular response")
	}
}

func TestIntegration_CORSOverride(t *testing.T) {
	gw := newTestGateway(t)
	req := httptest.NewRequest("GET", "/api/cors/data", nil)
	req.Header.Set("Origin", "https://override.example.com")
	rr := httptest.NewRecorder()
	gw.handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://override.example.com" {
		t.Errorf("expected override origin, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestIntegration_HealthEndpoint(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/health", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["status"] != "up" {
		t.Errorf("expected status=up, got %v", body["status"])
	}
	if body["timestamp"] == nil {
		t.Error("missing timestamp in health response")
	}
}

func TestIntegration_MetricsEndpoint(t *testing.T) {
	gw := newTestGateway(t)
	doRequest(gw.handler, "GET", "/public/metrics-gen", nil, "")

	rr := doRequest(gw.handler, "GET", "/metrics", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "gateway_requests_total") {
		t.Error("metrics should contain gateway_requests_total")
	}
	if !strings.Contains(body, "gateway_request_duration_seconds") {
		t.Error("metrics should contain gateway_request_duration_seconds")
	}
}

func TestIntegration_UnknownPath404(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/nonexistent/path", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["error"] != "not_found" {
		t.Errorf("expected error=not_found, got %v", body["error"])
	}
}

func TestIntegration_PanicRecovery(t *testing.T) {
	routes := []config.RouteConfig{{
		Name:  "panic-route",
		Match: config.MatchConfig{PathRegex: `^/panic$`},
		Upstream: config.UpstreamConfig{
			URL:     "http://127.0.0.1:1",
			Timeout: time.Second,
		},
	}}
	panicHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})
	handlers := map[string]http.Handler{"panic-route": panicHandler}
	r := router.New(routes, handlers)
	chain := middleware.Chain(middleware.Recovery(), middleware.RequestID())
	mux := http.NewServeMux()
	mux.Handle("/", chain(r))

	rr := doRequest(mux, "GET", "/panic", nil, "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["error"] != "internal_error" {
		t.Errorf("expected error=internal_error, got %v", body["error"])
	}
}

func TestIntegration_RequestIDPresent(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/public/reqid-test", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	reqID := rr.Header().Get("X-Request-ID")
	if reqID == "" {
		t.Error("missing X-Request-ID in response")
	}
	if len(reqID) < 32 {
		t.Errorf("X-Request-ID looks too short: %q", reqID)
	}
}

func TestIntegration_RequestIDPropagated(t *testing.T) {
	gw := newTestGateway(t)
	rr := doRequest(gw.handler, "GET", "/public/reqid-prop", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	responseID := rr.Header().Get("X-Request-ID")
	upstreamID := gw.upstream.lastRequest().Headers.Get("X-Request-ID")
	if responseID == "" || upstreamID == "" {
		t.Fatal("expected X-Request-ID on both response and upstream")
	}
	if responseID != upstreamID {
		t.Errorf("response ID %q != upstream ID %q", responseID, upstreamID)
	}
}

func TestIntegration_MethodFiltering(t *testing.T) {
	gw := newTestGateway(t)
	// test-public only allows GET; POST should not match any route → 404
	rr := doRequest(gw.handler, "POST", "/public/data", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong method, got %d", rr.Code)
	}
}
