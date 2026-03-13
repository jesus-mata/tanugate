package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jesus-mata/tanugate/internal/config"
)

func corsConfig(origins []string) config.CORSConfig {
	return config.CORSConfig{
		AllowedOrigins: origins,
		AllowedMethods: []string{"GET", "POST", "PUT"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		MaxAge:         3600,
	}
}

func TestPreflight_AllowedOrigin_Returns204(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	nextCalled := false
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	r := httptest.NewRequest("OPTIONS", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("expected origin header, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Access-Control-Allow-Methods") != "GET, POST, PUT" {
		t.Errorf("expected methods header, got %q", w.Header().Get("Access-Control-Allow-Methods"))
	}
	if nextCalled {
		t.Error("next handler should not be called for preflight")
	}
}

func TestPreflight_MaxAge(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	cfg.MaxAge = 7200
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	r := httptest.NewRequest("OPTIONS", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Max-Age") != "7200" {
		t.Errorf("expected max-age 7200, got %q", w.Header().Get("Access-Control-Max-Age"))
	}
}

func TestPreflight_Credentials(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	cfg.AllowCredentials = true
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	r := httptest.NewRequest("OPTIONS", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected credentials header, got %q", w.Header().Get("Access-Control-Allow-Credentials"))
	}
}

func TestRegularRequest_AllowedOrigin(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("expected origin header, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestRegularRequest_DisallowedOrigin(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no ACAO header, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", w.Header().Get("Vary"))
	}
}

func TestPreflight_DisallowedOrigin(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	r := httptest.NewRequest("OPTIONS", "/test", nil)
	r.Header.Set("Origin", "https://evil.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no ACAO header, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", w.Header().Get("Vary"))
	}
}

func TestWildcardOrigin(t *testing.T) {
	cfg := corsConfig([]string{"*"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://any-origin.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected *, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestWildcardOrigin_WithCredentials(t *testing.T) {
	cfg := corsConfig([]string{"*"})
	cfg.AllowCredentials = true
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	// Should use requesting origin instead of * when credentials are enabled
	if w.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("expected requesting origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected credentials header")
	}
}

func TestNoOriginHeader(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	nextCalled := false
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	// No Origin header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !nextCalled {
		t.Error("next handler should be called")
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS headers, got ACAO=%q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "" {
		t.Errorf("expected no Vary header, got %q", w.Header().Get("Vary"))
	}
}

func TestVaryOrigin_AlwaysSet(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Allowed origin
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("expected Vary: Origin for allowed origin, got %q", w.Header().Get("Vary"))
	}

	// Disallowed origin
	r = httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://evil.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("expected Vary: Origin for disallowed origin, got %q", w.Header().Get("Vary"))
	}
}

func TestMultipleAllowedOrigins(t *testing.T) {
	cfg := corsConfig([]string{"https://a.com", "https://b.com", "https://c.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://b.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Origin") != "https://b.com" {
		t.Errorf("expected https://b.com, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestPerRouteCORSOverride(t *testing.T) {
	globalCfg := corsConfig([]string{"https://global.com"})
	routeCfg := config.CORSConfig{
		AllowedOrigins: []string{"https://route.com"},
		AllowedMethods: []string{"GET"},
		AllowedHeaders: []string{"X-Custom"},
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORS(globalCfg)(CORSOverride(routeCfg)(inner))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://route.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Origin") != "https://route.com" {
		t.Errorf("expected route origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	// Marker should be cleaned up
	if w.Header().Get(corsOverrideMarker) != "" {
		t.Error("internal marker header should not leak to response")
	}
}

func TestPerRouteCORSOverride_CompleteReplacement(t *testing.T) {
	globalCfg := corsConfig([]string{"https://global.com"})
	globalCfg.AllowCredentials = true
	routeCfg := config.CORSConfig{
		AllowedOrigins: []string{"https://route.com"},
		AllowedMethods: []string{"GET"},
		// No AllowCredentials — should NOT inherit from global
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CORS(globalCfg)(CORSOverride(routeCfg)(inner))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://route.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("expected no credentials header (complete replacement), got %q",
			w.Header().Get("Access-Control-Allow-Credentials"))
	}
	// Global config should NOT have been applied
	if w.Header().Get("Access-Control-Allow-Origin") != "https://route.com" {
		t.Errorf("expected route origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestPreflight_NotActualPreflight(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	nextCalled := false
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	// OPTIONS with Origin but without Access-Control-Request-Method
	r := httptest.NewRequest("OPTIONS", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !nextCalled {
		t.Error("next handler should be called for non-preflight OPTIONS")
	}
}

func TestResponseWriterFlusher(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	flushed := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	handler := CORS(cfg)(inner)

	fw := &flushTracker{ResponseWriter: httptest.NewRecorder(), flushed: &flushed}
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	handler.ServeHTTP(fw, r)

	if !flushed {
		t.Error("Flush should delegate to underlying writer")
	}
}

type flushTracker struct {
	http.ResponseWriter
	flushed *bool
}

func (f *flushTracker) Flush() {
	*f.flushed = true
}

func TestCORSHeaders_NotDuplicated(t *testing.T) {
	cfg := corsConfig([]string{"https://example.com"})
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	// Headers should be set with Set(), not Add(), so no duplicates
	origins := w.Header().Values("Access-Control-Allow-Origin")
	if len(origins) != 1 {
		t.Errorf("expected 1 ACAO value, got %d: %v", len(origins), origins)
	}
}
