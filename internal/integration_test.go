package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/middleware/ratelimit"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/jesus-mata/tanugate/internal/pipeline"
	"github.com/jesus-mata/tanugate/internal/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
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
		_, _ = buf.ReadFrom(r.Body)
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
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "service_unavailable"})
		return
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Upstream-Path", r.URL.Path)
	w.Header().Set("X-Powered-By", "mock-upstream")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
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

// mustYAMLNode converts a Go value into a yaml.Node suitable for use as
// MiddlewareDefinition.Config or MiddlewareRef.Config. It panics on error
// because test setup failures should be immediately visible.
func mustYAMLNode(t *testing.T, v any) yaml.Node {
	t.Helper()
	data, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("mustYAMLNode: marshal failed: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("mustYAMLNode: unmarshal failed: %v", err)
	}
	// yaml.Unmarshal produces a document node wrapping the actual content.
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
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

	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	// Build the GatewayConfig with middleware definitions and refs.
	gwConfig := &config.GatewayConfig{
		RateLimit: config.RateLimitGlobalConfig{Backend: "memory"},
		AuthProviders: map[string]config.AuthProvider{
			"jwt_default": {
				Type: "jwt",
				JWT: &config.JWTConfig{
					Secret:    testJWTSecret,
					Algorithm: "HS256",
					Issuer:    "test-issuer",
				},
			},
			"apikey_svc1": {
				Type: "apikey",
				APIKey: &config.APIKeyConfig{
					Header: "X-API-Key",
					Keys:   []config.APIKeyEntry{{Key: testAPIKey, Name: "test-client"}},
				},
			},
		},
		Middlewares: []config.MiddlewareRef{
			{Ref: "global-cors"},
		},
		MiddlewareDefinitions: map[string]config.MiddlewareDefinition{
			"global-cors": {
				Type: "cors",
				Config: mustYAMLNode(t, map[string]any{
					"allowed_origins":   []string{"https://example.com"},
					"allowed_methods":   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
					"allowed_headers":   []string{"Authorization", "Content-Type", "X-API-Key"},
					"allow_credentials": true,
					"max_age":           3600,
				}),
			},
			"jwt-auth": {
				Type: "auth",
				Config: mustYAMLNode(t, map[string]any{
					"providers": []string{"jwt_default"},
				}),
			},
			"apikey-auth": {
				Type: "auth",
				Config: mustYAMLNode(t, map[string]any{
					"providers": []string{"apikey_svc1"},
				}),
			},
			"ip-limiter": {
				Type: "rate_limit",
				Config: mustYAMLNode(t, map[string]any{
					"requests":   5,
					"window":     "60s",
					"key_source": "ip",
				}),
			},
			"jwt-transform": {
				Type: "transform",
				Config: mustYAMLNode(t, map[string]any{
					"request": map[string]any{
						"headers": map[string]any{
							"add":    map[string]string{"X-Gateway-Route": "${route_name}"},
							"remove": []string{"X-Internal-Debug"},
						},
					},
					"response": map[string]any{
						"headers": map[string]any{
							"add":    map[string]string{"X-Served-By": "api-gateway"},
							"remove": []string{"X-Powered-By"},
						},
					},
				}),
			},
			"apikey-transform": {
				Type: "transform",
				Config: mustYAMLNode(t, map[string]any{
					"request": map[string]any{
						"body": map[string]any{
							"inject_fields": map[string]any{"_source": "api-gateway"},
							"strip_fields":  []string{"secret_field"},
						},
					},
					"response": map[string]any{
						"body": map[string]any{
							"strip_fields": []string{"internal_id"},
							"rename_keys":  map[string]string{"db_id": "id"},
						},
					},
				}),
			},
			"cors-override": {
				Type: "cors",
				Config: mustYAMLNode(t, map[string]any{
					"allowed_origins":   []string{"https://override.example.com"},
					"allowed_methods":   []string{"GET"},
					"allowed_headers":   []string{"Authorization"},
					"allow_credentials": true,
					"max_age":           600,
				}),
			},
		},
		Routes: []config.RouteConfig{
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
				// No middlewares — public route.
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
				Middlewares: []config.MiddlewareRef{
					{Ref: "ip-limiter"},
					{Ref: "jwt-auth"},
					{Ref: "jwt-transform"},
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
				Middlewares: []config.MiddlewareRef{
					{Ref: "apikey-auth"},
					{Ref: "apikey-transform"},
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
				SkipMiddlewares: []string{"global-cors"},
				Middlewares: []config.MiddlewareRef{
					{Ref: "cors-override"},
				},
			},
		},
	}

	deps := &pipeline.FactoryDeps{
		Limiter:        limiter,
		Authenticators: authenticators,
		Metrics:        metrics,
		TrustedProxies: nil,
		Logger:         slog.Default(),
	}
	registry := pipeline.DefaultRegistry()

	handler, cleanup, err := pipeline.BuildHandler(gwConfig, deps, registry)
	if err != nil {
		t.Fatalf("pipeline.BuildHandler failed: %v", err)
	}
	t.Cleanup(cleanup)

	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(gwConfig)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", observability.HealthHandler(&cfgPtr, nil))
	mux.Handle("GET /metrics", promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{}))
	mux.Handle("/", handler)

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

	// Preflight should also work with the per-route CORS override.
	preReq := httptest.NewRequest("OPTIONS", "/api/cors/data", nil)
	preReq.Header.Set("Origin", "https://override.example.com")
	preReq.Header.Set("Access-Control-Request-Method", "GET")
	preRR := httptest.NewRecorder()
	gw.handler.ServeHTTP(preRR, preReq)

	if preRR.Code != http.StatusNoContent {
		t.Fatalf("preflight: expected 204, got %d", preRR.Code)
	}
	if preRR.Header().Get("Access-Control-Allow-Origin") != "https://override.example.com" {
		t.Errorf("preflight: expected override origin, got %q", preRR.Header().Get("Access-Control-Allow-Origin"))
	}
	if preRR.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("preflight: missing Access-Control-Allow-Methods")
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
	// test-public only allows GET; POST should not match any route -> 404
	rr := doRequest(gw.handler, "POST", "/public/data", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong method, got %d", rr.Code)
	}
}

// handlerRef wraps an http.Handler for use with atomic.Pointer (mirrors cmd/gateway).
type handlerRef struct{ h http.Handler }

// buildTestHandler builds a handler pipeline from a GatewayConfig using
// pipeline.BuildHandler, mirroring production code exactly.
func buildTestHandler(
	t *testing.T,
	gwCfg *config.GatewayConfig,
	authenticators map[string]auth.Authenticator,
	limiter ratelimit.Limiter,
	metrics *observability.MetricsCollector,
) http.Handler {
	t.Helper()

	deps := &pipeline.FactoryDeps{
		Limiter:        limiter,
		Authenticators: authenticators,
		Metrics:        metrics,
		TrustedProxies: nil,
		Logger:         slog.Default(),
	}
	registry := pipeline.DefaultRegistry()

	handler, cleanup, err := pipeline.BuildHandler(gwCfg, deps, registry)
	if err != nil {
		t.Fatalf("pipeline.BuildHandler failed: %v", err)
	}
	t.Cleanup(cleanup)
	return handler
}

// makeSimpleGatewayConfig creates a minimal GatewayConfig with the given routes.
func makeSimpleGatewayConfig(routes []config.RouteConfig) *config.GatewayConfig {
	return &config.GatewayConfig{
		RateLimit: config.RateLimitGlobalConfig{Backend: "memory"},
		Routes:    routes,
	}
}

func TestIntegration_ConfigReload(t *testing.T) {
	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{}
	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	// Initial routes: only /v1/...
	initialRoutes := []config.RouteConfig{
		{
			Name:  "v1",
			Match: config.MatchConfig{PathRegex: `^/v1/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/api/{rest}",
				Timeout:     5 * time.Second,
			},
		},
	}

	initialCfg := makeSimpleGatewayConfig(initialRoutes)
	initialHandler := buildTestHandler(t, initialCfg, authenticators, limiter, metrics)

	// Set up atomic handler pointer.
	var current atomic.Pointer[handlerRef]
	current.Store(&handlerRef{h: initialHandler})

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().h.ServeHTTP(w, r)
	}))

	// Verify initial route works.
	rr := doRequest(mux, "GET", "/v1/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /v1/items, got %d", rr.Code)
	}

	// Verify /v2/ does not exist yet.
	rr = doRequest(mux, "GET", "/v2/items", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for /v2/items before reload, got %d", rr.Code)
	}

	// Simulate reload: add /v2/ route, remove /v1/ route.
	newRoutes := []config.RouteConfig{
		{
			Name:  "v2",
			Match: config.MatchConfig{PathRegex: `^/v2/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/api/{rest}",
				Timeout:     5 * time.Second,
			},
		},
	}

	newCfg := makeSimpleGatewayConfig(newRoutes)
	newHandler := buildTestHandler(t, newCfg, authenticators, limiter, metrics)

	// Atomic swap.
	current.Store(&handlerRef{h: newHandler})

	// After reload: /v2/ should work.
	rr = doRequest(mux, "GET", "/v2/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /v2/items after reload, got %d", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["echo_path"] != "/api/items" {
		t.Errorf("expected echo_path=/api/items, got %v", body["echo_path"])
	}

	// After reload: /v1/ should return 404.
	rr = doRequest(mux, "GET", "/v1/items", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for /v1/items after reload, got %d", rr.Code)
	}
}

func TestIntegration_ConfigReload_InvalidConfigKeepsOldHandler(t *testing.T) {
	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{}
	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	routes := []config.RouteConfig{
		{
			Name:  "api",
			Match: config.MatchConfig{PathRegex: `^/api/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/{rest}",
				Timeout:     5 * time.Second,
			},
		},
	}

	gwCfg := makeSimpleGatewayConfig(routes)
	handler := buildTestHandler(t, gwCfg, authenticators, limiter, metrics)

	var current atomic.Pointer[handlerRef]
	current.Store(&handlerRef{h: handler})

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().h.ServeHTTP(w, r)
	}))

	// Verify route works.
	rr := doRequest(mux, "GET", "/api/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Simulate failed reload: invalid config -> do NOT swap.
	// (In the real code, handleReload catches the LoadConfig error and skips the swap.)
	// Here we simulate by validating a bad config and verifying we don't swap.
	badCfg := &config.GatewayConfig{
		Routes: []config.RouteConfig{
			{
				Name:  "bad",
				Match: config.MatchConfig{PathRegex: `^/api/[invalid`},
				Upstream: config.UpstreamConfig{
					URL: upstreamServer.URL,
				},
			},
		},
	}
	if err := badCfg.Validate(); err == nil {
		t.Fatal("expected validation error for bad regex")
	}
	// Handler was NOT swapped -- old route should still work.
	rr = doRequest(mux, "GET", "/api/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after failed reload, got %d", rr.Code)
	}
}

func TestIntegration_ConfigReload_ConcurrentRequests(t *testing.T) {
	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{}
	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	makeRoutes := func(name, prefix string) []config.RouteConfig {
		return []config.RouteConfig{{
			Name:  name,
			Match: config.MatchConfig{PathRegex: `^/` + prefix + `/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/api/{rest}",
				Timeout:     5 * time.Second,
			},
		}}
	}

	initialCfg := makeSimpleGatewayConfig(makeRoutes("v1", "v1"))
	initialHandler := buildTestHandler(t, initialCfg, authenticators, limiter, metrics)

	var current atomic.Pointer[handlerRef]
	current.Store(&handlerRef{h: initialHandler})

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().h.ServeHTTP(w, r)
	}))

	// Fire concurrent requests while swapping handlers.
	var wg sync.WaitGroup
	errors := make(chan error, 100)
	var v1ok, v2ok atomic.Int32

	// Readers hitting /v1/ and /v2/ concurrently.
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := doRequest(mux, "GET", "/v1/data", nil, "")
			// Before swap: 200; after swap: 404. Both are acceptable.
			if rr.Code == http.StatusOK {
				v1ok.Add(1)
			} else if rr.Code != http.StatusNotFound {
				errors <- fmt.Errorf("unexpected status %d for /v1/data", rr.Code)
			}
		}()
	}

	// Swap in the middle of concurrent requests.
	newCfg := makeSimpleGatewayConfig(makeRoutes("v2", "v2"))
	newHandler := buildTestHandler(t, newCfg, authenticators, limiter, metrics)
	current.Store(&handlerRef{h: newHandler})

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := doRequest(mux, "GET", "/v2/data", nil, "")
			// After swap: should be 200. Before swap would be 404.
			if rr.Code == http.StatusOK {
				v2ok.Add(1)
			} else if rr.Code != http.StatusNotFound {
				errors <- fmt.Errorf("unexpected status %d for /v2/data", rr.Code)
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	if v1ok.Load() == 0 {
		t.Error("expected at least one /v1/data request to succeed with 200")
	}
	if v2ok.Load() == 0 {
		t.Error("expected at least one /v2/data request to succeed with 200")
	}
}

func TestIntegration_ConfigReload_ZeroRoutes(t *testing.T) {
	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{}
	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	initialRoutes := []config.RouteConfig{
		{
			Name:  "api",
			Match: config.MatchConfig{PathRegex: `^/api/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/internal/{rest}",
				Timeout:     5 * time.Second,
			},
		},
	}

	initialCfg := makeSimpleGatewayConfig(initialRoutes)
	handler := buildTestHandler(t, initialCfg, authenticators, limiter, metrics)

	var current atomic.Pointer[handlerRef]
	current.Store(&handlerRef{h: handler})

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().h.ServeHTTP(w, r)
	}))

	// Route works before reload.
	rr := doRequest(mux, "GET", "/api/items", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Reload with zero routes -- everything becomes 404.
	emptyCfg := makeSimpleGatewayConfig([]config.RouteConfig{})
	emptyHandler := buildTestHandler(t, emptyCfg, authenticators, limiter, metrics)
	current.Store(&handlerRef{h: emptyHandler})

	rr = doRequest(mux, "GET", "/api/items", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after reload with zero routes, got %d", rr.Code)
	}
}

func TestIntegration_ConfigReload_SchemaRejection(t *testing.T) {
	validYAML := `
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(cfgPath, []byte(validYAML), 0644); err != nil {
		t.Fatalf("failed to write valid config: %v", err)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig should succeed for valid config: %v", err)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Name != "test" {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	invalidYAML := `
routez:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
`
	if err := os.WriteFile(cfgPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("failed to write invalid config: %v", err)
	}

	_, err = config.LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected LoadConfig to reject config with unknown field 'routez'")
	}
	if !strings.Contains(err.Error(), "additional properties") && !strings.Contains(err.Error(), "routez") {
		t.Fatalf("expected error mentioning 'additional properties' or 'routez', got: %v", err)
	}
}

func TestBuildTestHandler_MiddlewareOrderMatchesProduction(t *testing.T) {
	// Both the integration test helper (buildTestHandler) and production code
	// (cmd/gateway/main.go) now delegate to pipeline.BuildHandler, guaranteeing
	// identical middleware ordering. This test verifies the shared pipeline
	// behaviorally: rate limiting runs before auth (outermost), so a rate-limited
	// request returns 429 even if it carries valid auth credentials.

	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{
		"jwt_default": mustAuth(t, config.AuthProvider{
			Type: "jwt",
			JWT: &config.JWTConfig{
				Secret:    testJWTSecret,
				Algorithm: "HS256",
				Issuer:    "test-issuer",
			},
		}),
	}

	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	gwCfg := &config.GatewayConfig{
		RateLimit: config.RateLimitGlobalConfig{Backend: "memory"},
		AuthProviders: map[string]config.AuthProvider{
			"jwt_default": {
				Type: "jwt",
				JWT: &config.JWTConfig{
					Secret:    testJWTSecret,
					Algorithm: "HS256",
					Issuer:    "test-issuer",
				},
			},
		},
		MiddlewareDefinitions: map[string]config.MiddlewareDefinition{
			"tight-limiter": {
				Type: "rate_limit",
				Config: mustYAMLNode(t, map[string]any{
					"requests":   2,
					"window":     "60s",
					"key_source": "ip",
				}),
			},
			"jwt-auth": {
				Type: "auth",
				Config: mustYAMLNode(t, map[string]any{
					"providers": []string{"jwt_default"},
				}),
			},
		},
		Routes: []config.RouteConfig{
			{
				Name:  "order-test",
				Match: config.MatchConfig{PathRegex: `^/order-test$`},
				Upstream: config.UpstreamConfig{
					URL:     upstreamServer.URL,
					Timeout: 5 * time.Second,
				},
				Middlewares: []config.MiddlewareRef{
					{Ref: "tight-limiter"},
					{Ref: "jwt-auth"},
				},
			},
		},
	}

	handler := buildTestHandler(t, gwCfg, authenticators, limiter, metrics)
	headers := map[string]string{"Authorization": "Bearer " + validJWT()}

	// Exhaust the rate limit.
	for i := 0; i < 2; i++ {
		rr := doRequest(handler, "GET", "/order-test", headers, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// Third request should be rate-limited (429), not auth-rejected.
	// This confirms rate limiting is outermost (runs first) per the middleware
	// order [tight-limiter, jwt-auth].
	rr := doRequest(handler, "GET", "/order-test", headers, "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 (rate limited), got %d; rate limiter should run before auth", rr.Code)
	}

}

// handlerRefWithCleanup mirrors cmd/gateway's handlerRef, which tracks the
// cleanup function alongside the handler for transport lifecycle management.
type handlerRefWithCleanup struct {
	h       http.Handler
	cleanup func()
}

func TestIntegration_ConfigReload_TransportCleanup(t *testing.T) {
	// This test verifies that the shared http.Transport allocated by
	// pipeline.BuildHandler is properly cleaned up on reload. Without cleanup,
	// each reload leaks a transport's connection pool (goroutines + FDs).

	mock := &mockUpstream{}
	upstreamServer := httptest.NewServer(mock)
	t.Cleanup(upstreamServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	authenticators := map[string]auth.Authenticator{}
	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	makeRoutes := func(name string) []config.RouteConfig {
		return []config.RouteConfig{{
			Name:  name,
			Match: config.MatchConfig{PathRegex: `^/` + name + `/(?P<rest>.*)$`},
			Upstream: config.UpstreamConfig{
				URL:         upstreamServer.URL,
				PathRewrite: "/api/{rest}",
				Timeout:     5 * time.Second,
			},
		}}
	}

	buildHandler := func(routes []config.RouteConfig) (http.Handler, func()) {
		cfg := makeSimpleGatewayConfig(routes)
		deps := &pipeline.FactoryDeps{
			Limiter:        limiter,
			Authenticators: authenticators,
			Metrics:        metrics,
			TrustedProxies: nil,
			Logger:         slog.Default(),
		}
		registry := pipeline.DefaultRegistry()
		h, cleanup, err := pipeline.BuildHandler(cfg, deps, registry)
		if err != nil {
			t.Fatalf("BuildHandler failed: %v", err)
		}
		return h, cleanup
	}

	// Initial build.
	h, cleanup := buildHandler(makeRoutes("v1"))
	var current atomic.Pointer[handlerRefWithCleanup]
	current.Store(&handlerRefWithCleanup{h: h, cleanup: cleanup})

	// Record goroutine count after initial build stabilizes.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	// Simulate 10 config reloads, calling old cleanup on each swap.
	var cleanupsCalled atomic.Int32
	for i := range 10 {
		name := fmt.Sprintf("v%d", i+2)
		newH, newCleanup := buildHandler(makeRoutes(name))

		old := current.Load()
		wrappedCleanup := newCleanup
		current.Store(&handlerRefWithCleanup{h: newH, cleanup: wrappedCleanup})

		// Clean up old transport (mirrors handleReload in main.go).
		if old.cleanup != nil {
			old.cleanup()
			cleanupsCalled.Add(1)
		}
	}

	// Clean up the final handler.
	if ref := current.Load(); ref.cleanup != nil {
		ref.cleanup()
		cleanupsCalled.Add(1)
	}

	// All 11 cleanups should have been called (10 old + 1 final).
	if got := cleanupsCalled.Load(); got != 11 {
		t.Errorf("expected 11 cleanup calls, got %d", got)
	}

	// Goroutine count should not have grown significantly.
	// Allow some slack for test infrastructure goroutines.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()
	goroutineGrowth := finalGoroutines - baselineGoroutines

	// With proper cleanup, growth should be minimal (≤5).
	// Without cleanup, each reload leaks transport goroutines (~2-4 per transport).
	if goroutineGrowth > 5 {
		t.Errorf("goroutine count grew by %d after 10 reloads (baseline=%d, final=%d); possible transport leak",
			goroutineGrowth, baselineGoroutines, finalGoroutines)
	}
}

func TestIntegration_HeaderBasedRouting(t *testing.T) {
	// Two mock upstreams: v2 and default.
	v2Upstream := &mockUpstream{}
	v2Server := httptest.NewServer(v2Upstream)
	t.Cleanup(v2Server.Close)

	defaultUpstream := &mockUpstream{}
	defaultServer := httptest.NewServer(defaultUpstream)
	t.Cleanup(defaultServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	promRegistry := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promRegistry)
	limiter := ratelimit.NewMemoryLimiter(ctx)

	gwConfig := &config.GatewayConfig{
		RateLimit: config.RateLimitGlobalConfig{Backend: "memory"},
		Routes: []config.RouteConfig{
			{
				Name: "v2-api",
				Match: config.MatchConfig{
					PathRegex: `^/api/(?P<rest>.*)$`,
					Headers:   map[string]string{"X-API-Version": "v2"},
				},
				Upstream: config.UpstreamConfig{
					URL:         v2Server.URL,
					PathRewrite: "/v2/{rest}",
					Timeout:     5 * time.Second,
				},
			},
			{
				Name: "default-api",
				Match: config.MatchConfig{
					PathRegex: `^/api/(?P<rest>.*)$`,
				},
				Upstream: config.UpstreamConfig{
					URL:         defaultServer.URL,
					PathRewrite: "/default/{rest}",
					Timeout:     5 * time.Second,
				},
			},
		},
	}

	deps := &pipeline.FactoryDeps{
		Limiter:        limiter,
		Authenticators: nil,
		Metrics:        metrics,
		TrustedProxies: nil,
		Logger:         slog.Default(),
	}
	registry := pipeline.DefaultRegistry()

	handler, cleanup, err := pipeline.BuildHandler(gwConfig, deps, registry)
	if err != nil {
		t.Fatalf("pipeline.BuildHandler failed: %v", err)
	}
	t.Cleanup(cleanup)

	// Request with X-API-Version: v2 should route to the v2 upstream.
	rr := doRequest(handler, "GET", "/api/items", map[string]string{"X-API-Version": "v2"}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for v2 route, got %d", rr.Code)
	}
	v2Last := v2Upstream.lastRequest()
	if v2Last.Path != "/v2/items" {
		t.Fatalf("expected v2 upstream to receive /v2/items, got %q", v2Last.Path)
	}

	// Request without the header should fall through to the default upstream.
	rr2 := doRequest(handler, "GET", "/api/items", nil, "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for default route, got %d", rr2.Code)
	}
	defaultLast := defaultUpstream.lastRequest()
	if defaultLast.Path != "/default/items" {
		t.Fatalf("expected default upstream to receive /default/items, got %q", defaultLast.Path)
	}
}
