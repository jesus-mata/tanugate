package transform

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/router"
)

// --- resolveVariables tests ---

func TestResolveVariables(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/users", nil)
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 192.168.1.1")
	r.Header.Set("X-Request-ID", "test-req-id")
	// Use RequestID middleware to set the ID in context.
	var captured *http.Request
	middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		captured = req
	})).ServeHTTP(httptest.NewRecorder(), r)
	r = captured
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{Name: "users-route"},
	})
	r = r.WithContext(ctx)

	latency := int64(42)

	tests := []struct {
		name     string
		input    string
		latency  *int64
		contains string // substring check (for timestamps)
		exact    string // exact match
	}{
		{"static string", "hello world", nil, "", "hello world"},
		{"request_id", "${request_id}", nil, "", "test-req-id"},
		{"client_ip", "${client_ip}", nil, "", "10.0.0.1"},
		{"method", "${method}", nil, "", "POST"},
		{"path", "${path}", nil, "", "/api/users"},
		{"route_name", "${route_name}", nil, "", "users-route"},
		{"latency_ms", "${latency_ms}", &latency, "", "42"},
		{"latency_ms nil", "${latency_ms}", nil, "", "0"},
		{"timestamp_iso", "${timestamp_iso}", nil, "T", ""},
		{"timestamp_unix", "${timestamp_unix}", nil, "", ""},
		{"multiple vars", "${method} ${path}", nil, "", "POST /api/users"},
		{"unknown var", "${unknown_var}", nil, "", "${unknown_var}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveVariables(tt.input, r, tt.latency)
			if tt.exact != "" && result != tt.exact {
				t.Errorf("got %q, want %q", result, tt.exact)
			}
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("got %q, want substring %q", result, tt.contains)
			}
			if tt.name == "timestamp_unix" {
				if _, err := strconv.ParseInt(result, 10, 64); err != nil {
					t.Errorf("timestamp_unix %q is not a valid int64", result)
				}
			}
		})
	}
}

func TestResolveVariables_ClientIP_RemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.100:54321"
	result := resolveVariables("${client_ip}", r, nil)
	if result != "192.168.1.100" {
		t.Errorf("got %q, want %q", result, "192.168.1.100")
	}
}

func TestResolveVariables_ClientIP_IPv6(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::1]:8080"
	result := resolveVariables("${client_ip}", r, nil)
	if result != "::1" {
		t.Errorf("got %q, want %q", result, "::1")
	}
}

// --- transformHeaders tests ---

func TestTransformHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)

	t.Run("nil config", func(t *testing.T) {
		h := http.Header{"X-Keep": {"val"}}
		transformHeaders(h, nil, r, nil)
		if h.Get("X-Keep") != "val" {
			t.Error("nil config should be no-op")
		}
	})

	t.Run("remove", func(t *testing.T) {
		h := http.Header{"X-Secret": {"s3cret"}, "X-Keep": {"val"}}
		transformHeaders(h, &config.HeaderTransform{
			Remove: []string{"X-Secret"},
		}, r, nil)
		if h.Get("X-Secret") != "" {
			t.Error("header should be removed")
		}
		if h.Get("X-Keep") != "val" {
			t.Error("unrelated header should remain")
		}
	})

	t.Run("rename", func(t *testing.T) {
		h := http.Header{"X-Old": {"v1", "v2"}}
		transformHeaders(h, &config.HeaderTransform{
			Rename: map[string]string{"X-Old": "X-New"},
		}, r, nil)
		if h.Get("X-Old") != "" {
			t.Error("old header should be removed")
		}
		vals := h.Values("X-New")
		if len(vals) != 2 || vals[0] != "v1" || vals[1] != "v2" {
			t.Errorf("renamed header values = %v, want [v1 v2]", vals)
		}
	})

	t.Run("rename non-existent", func(t *testing.T) {
		h := http.Header{"X-Foo": {"bar"}}
		transformHeaders(h, &config.HeaderTransform{
			Rename: map[string]string{"X-Missing": "X-New"},
		}, r, nil)
		if h.Get("X-New") != "" {
			t.Error("renaming non-existent header should be no-op")
		}
	})

	t.Run("add", func(t *testing.T) {
		h := http.Header{}
		transformHeaders(h, &config.HeaderTransform{
			Add: map[string]string{"X-Custom": "hello"},
		}, r, nil)
		if h.Get("X-Custom") != "hello" {
			t.Errorf("got %q, want %q", h.Get("X-Custom"), "hello")
		}
	})

	t.Run("add with variable", func(t *testing.T) {
		h := http.Header{}
		transformHeaders(h, &config.HeaderTransform{
			Add: map[string]string{"X-Method": "${method}"},
		}, r, nil)
		if h.Get("X-Method") != "GET" {
			t.Errorf("got %q, want %q", h.Get("X-Method"), "GET")
		}
	})

	t.Run("order remove then rename then add", func(t *testing.T) {
		h := http.Header{"X-A": {"1"}, "X-B": {"2"}}
		transformHeaders(h, &config.HeaderTransform{
			Remove: []string{"X-A"},
			Rename: map[string]string{"X-B": "X-C"},
			Add:    map[string]string{"X-D": "4"},
		}, r, nil)
		if h.Get("X-A") != "" {
			t.Error("X-A should be removed")
		}
		if h.Get("X-B") != "" {
			t.Error("X-B should be renamed")
		}
		if h.Get("X-C") != "2" {
			t.Errorf("X-C = %q, want %q", h.Get("X-C"), "2")
		}
		if h.Get("X-D") != "4" {
			t.Errorf("X-D = %q, want %q", h.Get("X-D"), "4")
		}
	})

	t.Run("remove non-existent", func(t *testing.T) {
		h := http.Header{"X-Keep": {"val"}}
		transformHeaders(h, &config.HeaderTransform{
			Remove: []string{"X-Missing"},
		}, r, nil)
		if h.Get("X-Keep") != "val" {
			t.Error("should not affect existing headers")
		}
	})
}

// --- transformBody tests ---

func TestTransformBody(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)

	t.Run("nil config", func(t *testing.T) {
		body := []byte(`{"a":1}`)
		result, err := transformBody(body, nil, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result, body) {
			t.Error("nil config should return body unchanged")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		result, err := transformBody([]byte{}, &config.BodyTransform{
			StripFields: []string{"a"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 0 {
			t.Error("empty body should remain empty")
		}
	})

	t.Run("strip fields", func(t *testing.T) {
		body := []byte(`{"a":1,"b":2,"c":3}`)
		result, err := transformBody(body, &config.BodyTransform{
			StripFields: []string{"a", "c"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		if _, ok := data["a"]; ok {
			t.Error("field 'a' should be stripped")
		}
		if _, ok := data["c"]; ok {
			t.Error("field 'c' should be stripped")
		}
		if data["b"] != float64(2) {
			t.Error("field 'b' should remain")
		}
	})

	t.Run("rename keys", func(t *testing.T) {
		body := []byte(`{"old_name":"val"}`)
		result, err := transformBody(body, &config.BodyTransform{
			RenameKeys: map[string]string{"old_name": "new_name"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		if _, ok := data["old_name"]; ok {
			t.Error("old key should be removed")
		}
		if data["new_name"] != "val" {
			t.Error("value should be moved to new key")
		}
	})

	t.Run("inject fields", func(t *testing.T) {
		body := []byte(`{"existing":"val"}`)
		result, err := transformBody(body, &config.BodyTransform{
			InjectFields: map[string]any{"injected": "new_val"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		if data["injected"] != "new_val" {
			t.Errorf("injected = %v, want %q", data["injected"], "new_val")
		}
		if data["existing"] != "val" {
			t.Error("existing field should remain")
		}
	})

	t.Run("inject with variable", func(t *testing.T) {
		body := []byte(`{"a":1}`)
		result, err := transformBody(body, &config.BodyTransform{
			InjectFields: map[string]any{"method": "${method}"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		if data["method"] != "GET" {
			t.Errorf("method = %v, want %q", data["method"], "GET")
		}
	})

	t.Run("order strip then rename then inject", func(t *testing.T) {
		body := []byte(`{"remove_me":1,"rename_me":"val","keep":"ok"}`)
		result, err := transformBody(body, &config.BodyTransform{
			StripFields:  []string{"remove_me"},
			RenameKeys:   map[string]string{"rename_me": "renamed"},
			InjectFields: map[string]any{"added": "new"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		if _, ok := data["remove_me"]; ok {
			t.Error("remove_me should be stripped")
		}
		if _, ok := data["rename_me"]; ok {
			t.Error("rename_me should be renamed")
		}
		if data["renamed"] != "val" {
			t.Error("renamed key should have the value")
		}
		if data["added"] != "new" {
			t.Error("added field should exist")
		}
		if data["keep"] != "ok" {
			t.Error("keep should remain")
		}
	})

	t.Run("nested JSON only top-level affected", func(t *testing.T) {
		body := []byte(`{"top":"val","nested":{"inner":"keep"}}`)
		result, err := transformBody(body, &config.BodyTransform{
			StripFields: []string{"inner"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(result, &data)
		nested := data["nested"].(map[string]any)
		if _, ok := nested["inner"]; !ok {
			t.Error("nested 'inner' should NOT be affected")
		}
	})

	t.Run("non-JSON body unchanged", func(t *testing.T) {
		body := []byte("plain text body")
		result, err := transformBody(body, &config.BodyTransform{
			StripFields: []string{"a"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result, body) {
			t.Error("non-JSON body should pass through unchanged")
		}
	})

	t.Run("JSON array body unchanged", func(t *testing.T) {
		body := []byte(`[1,2,3]`)
		result, err := transformBody(body, &config.BodyTransform{
			StripFields: []string{"a"},
		}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result, body) {
			t.Error("JSON array body should pass through unchanged")
		}
	})
}

// --- bufferingResponseWriter tests ---

func TestBufferingResponseWriter(t *testing.T) {
	t.Run("header capture", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header)}
		buf.Header().Set("X-Test", "value")
		if buf.Header().Get("X-Test") != "value" {
			t.Error("header not captured")
		}
	})

	t.Run("status capture", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		buf.WriteHeader(http.StatusNotFound)
		if buf.statusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", buf.statusCode, http.StatusNotFound)
		}
	})

	t.Run("body buffering", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		_, _ = buf.Write([]byte("hello"))
		if buf.body.String() != "hello" {
			t.Errorf("body = %q, want %q", buf.body.String(), "hello")
		}
	})

	t.Run("multiple writes accumulate", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		_, _ = buf.Write([]byte("hello "))
		_, _ = buf.Write([]byte("world"))
		if buf.body.String() != "hello world" {
			t.Errorf("body = %q, want %q", buf.body.String(), "hello world")
		}
	})

	t.Run("implicit 200 status", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		_, _ = buf.Write([]byte("body"))
		if buf.statusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", buf.statusCode, http.StatusOK)
		}
	})

	t.Run("double WriteHeader ignored", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		buf.WriteHeader(http.StatusCreated)
		buf.WriteHeader(http.StatusNotFound) // should be ignored
		if buf.statusCode != http.StatusCreated {
			t.Errorf("status = %d, want %d", buf.statusCode, http.StatusCreated)
		}
	})

	t.Run("implements http.Flusher", func(t *testing.T) {
		buf := &bufferingResponseWriter{headers: make(http.Header), statusCode: http.StatusOK}
		var flusher http.Flusher = buf // compile-time check
		flusher.Flush()                // should not panic
	})
}

// --- RequestTransform middleware tests ---

func TestRequestTransformMiddleware(t *testing.T) {
	t.Run("nil config passthrough", func(t *testing.T) {
		handler := RequestTransform(nil, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Value(startTimeKey{}).(time.Time); !ok {
				t.Error("start time should be set even with nil config")
			}
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("header transform", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Headers: &config.HeaderTransform{
				Add:    map[string]string{"X-Added": "val"},
				Remove: []string{"X-Remove"},
			},
		}
		handler := RequestTransform(cfg, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Added") != "val" {
				t.Error("X-Added header not set")
			}
			if r.Header.Get("X-Remove") != "" {
				t.Error("X-Remove header should be removed")
			}
		}))
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Remove", "bye")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	})

	t.Run("body transform JSON", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Body: &config.BodyTransform{
				StripFields:  []string{"secret"},
				InjectFields: map[string]any{"added": "val"},
			},
		}
		handler := RequestTransform(cfg, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var data map[string]any
			_ = json.Unmarshal(body, &data)
			if _, ok := data["secret"]; ok {
				t.Error("secret should be stripped")
			}
			if data["added"] != "val" {
				t.Error("added should be injected")
			}
			cl := r.Header.Get("Content-Length")
			if cl != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length = %s, want %d", cl, len(body))
			}
		}))
		body := `{"secret":"s3cret","keep":"val"}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	})

	t.Run("non-JSON body unchanged", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Body: &config.BodyTransform{
				StripFields: []string{"a"},
			},
		}
		original := "plain text"
		handler := RequestTransform(cfg, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if string(body) != original {
				t.Errorf("body = %q, want %q", string(body), original)
			}
		}))
		req := httptest.NewRequest("POST", "/", strings.NewReader(original))
		req.Header.Set("Content-Type", "text/plain")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	})

	t.Run("start time stored in context", func(t *testing.T) {
		before := time.Now()
		handler := RequestTransform(nil, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start, ok := StartTimeFromContext(r.Context())
			if !ok {
				t.Fatal("start time not found in context")
			}
			if start.Before(before) {
				t.Error("start time should be >= test start")
			}
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	})
}

// --- ResponseTransform middleware tests ---

func TestResponseTransformMiddleware(t *testing.T) {
	t.Run("nil config passthrough", func(t *testing.T) {
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream", "val")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("body"))
		})
		handler := ResponseTransform(nil, 0)(upstream)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
		}
		if rec.Body.String() != "body" {
			t.Errorf("body = %q, want %q", rec.Body.String(), "body")
		}
	})

	t.Run("header transform", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Headers: &config.HeaderTransform{
				Add:    map[string]string{"X-Added": "val"},
				Remove: []string{"X-Remove"},
			},
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Remove", "bye")
			w.Header().Set("X-Keep", "stay")
			_, _ = w.Write([]byte("ok"))
		})
		handler := ResponseTransform(cfg, 0)(upstream)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Header().Get("X-Added") != "val" {
			t.Error("X-Added should be set")
		}
		if rec.Header().Get("X-Remove") != "" {
			t.Error("X-Remove should be removed")
		}
		if rec.Header().Get("X-Keep") != "stay" {
			t.Error("X-Keep should remain")
		}
	})

	t.Run("body transform JSON", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Body: &config.BodyTransform{
				StripFields:  []string{"secret"},
				InjectFields: map[string]any{"added": "val"},
			},
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"secret":"s3cret","keep":"val"}`))
		})
		handler := ResponseTransform(cfg, 0)(upstream)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		var data map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &data)
		if _, ok := data["secret"]; ok {
			t.Error("secret should be stripped")
		}
		if data["added"] != "val" {
			t.Error("added should be injected")
		}
		cl := rec.Header().Get("Content-Length")
		if cl != strconv.Itoa(rec.Body.Len()) {
			t.Errorf("Content-Length = %s, want %d", cl, rec.Body.Len())
		}
	})

	t.Run("status code preserved", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Headers: &config.HeaderTransform{
				Add: map[string]string{"X-Test": "val"},
			},
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		})
		handler := ResponseTransform(cfg, 0)(upstream)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("latency_ms available", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Headers: &config.HeaderTransform{
				Add: map[string]string{"X-Latency": "${latency_ms}"},
			},
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(5 * time.Millisecond)
			_, _ = w.Write([]byte("ok"))
		})
		// Wrap with RequestTransform(nil) to set start time in context.
		handler := RequestTransform(nil, 0)(ResponseTransform(cfg, 0)(upstream))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		latency := rec.Header().Get("X-Latency")
		ms, err := strconv.ParseInt(latency, 10, 64)
		if err != nil {
			t.Fatalf("X-Latency %q not an int: %v", latency, err)
		}
		if ms < 5 {
			t.Errorf("latency = %d, want >= 5", ms)
		}
	})

	t.Run("non-JSON response unchanged", func(t *testing.T) {
		cfg := &config.DirectionTransform{
			Body: &config.BodyTransform{
				StripFields: []string{"a"},
			},
		}
		original := "plain text response"
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(original))
		})
		handler := ResponseTransform(cfg, 0)(upstream)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Body.String() != original {
			t.Errorf("body = %q, want %q", rec.Body.String(), original)
		}
	})
}

// --- Integration test ---

func TestIntegration_RequestAndResponseTransform(t *testing.T) {
	reqCfg := &config.DirectionTransform{
		Headers: &config.HeaderTransform{
			Add:    map[string]string{"X-Req-Added": "${method}"},
			Remove: []string{"X-Req-Remove"},
		},
		Body: &config.BodyTransform{
			StripFields:  []string{"password"},
			InjectFields: map[string]any{"source": "gateway"},
		},
	}
	resCfg := &config.DirectionTransform{
		Headers: &config.HeaderTransform{
			Add:    map[string]string{"X-Res-Added": "done"},
			Remove: []string{"X-Internal"},
		},
		Body: &config.BodyTransform{
			StripFields:  []string{"internal_id"},
			RenameKeys:   map[string]string{"old_field": "new_field"},
			InjectFields: map[string]any{"gateway_latency": "${latency_ms}"},
		},
	}

	// Echo handler: reads request body, echoes it as response with extra headers.
	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request transforms were applied.
		if r.Header.Get("X-Req-Added") != "POST" {
			t.Errorf("X-Req-Added = %q, want %q", r.Header.Get("X-Req-Added"), "POST")
		}
		if r.Header.Get("X-Req-Remove") != "" {
			t.Error("X-Req-Remove should be removed from request")
		}

		body, _ := io.ReadAll(r.Body)
		var reqData map[string]any
		_ = json.Unmarshal(body, &reqData)
		if _, ok := reqData["password"]; ok {
			t.Error("password should be stripped from request body")
		}
		if reqData["source"] != "gateway" {
			t.Error("source should be injected into request body")
		}

		// Send response.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Internal", "secret")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"internal_id": 123,
			"old_field":   "value",
			"data":        "result",
		})
	})

	handler := RequestTransform(reqCfg, 0)(ResponseTransform(resCfg, 0)(echoHandler))

	reqBody := `{"username":"user","password":"pass","keep":"yes"}`
	req := httptest.NewRequest("POST", "/api/test", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Req-Remove", "should-go")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Verify response transforms.
	if rec.Header().Get("X-Res-Added") != "done" {
		t.Error("X-Res-Added should be set on response")
	}
	if rec.Header().Get("X-Internal") != "" {
		t.Error("X-Internal should be removed from response")
	}

	var resData map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resData); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	if _, ok := resData["internal_id"]; ok {
		t.Error("internal_id should be stripped from response body")
	}
	if _, ok := resData["old_field"]; ok {
		t.Error("old_field should be renamed")
	}
	if resData["new_field"] != "value" {
		t.Errorf("new_field = %v, want %q", resData["new_field"], "value")
	}
	if resData["data"] != "result" {
		t.Error("data should remain in response")
	}
	if _, ok := resData["gateway_latency"]; !ok {
		t.Error("gateway_latency should be injected")
	}
}

// --- Body size limit tests ---

func TestEffectiveMaxBody(t *testing.T) {
	if got := effectiveMaxBody(0); got != defaultMaxTransformBodySize {
		t.Errorf("effectiveMaxBody(0) = %d, want %d", got, defaultMaxTransformBodySize)
	}
	if got := effectiveMaxBody(1024); got != 1024 {
		t.Errorf("effectiveMaxBody(1024) = %d, want 1024", got)
	}
	if got := effectiveMaxBody(-1); got != defaultMaxTransformBodySize {
		t.Errorf("effectiveMaxBody(-1) = %d, want %d", got, defaultMaxTransformBodySize)
	}
}

func TestRequestTransform_BodySizeLimit(t *testing.T) {
	cfg := &config.DirectionTransform{
		Body: &config.BodyTransform{
			InjectFields: map[string]any{"added": "val"},
		},
	}
	const limit int64 = 64
	bigBody := `{"data":"` + strings.Repeat("x", int(limit)) + `"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler := RequestTransform(cfg, limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for oversized body")
	}))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "request_too_large" {
		t.Errorf("error = %q, want %q", resp["error"], "request_too_large")
	}
}

func TestRequestTransform_BodyWithinLimit(t *testing.T) {
	cfg := &config.DirectionTransform{
		Body: &config.BodyTransform{
			InjectFields: map[string]any{"added": "val"},
		},
	}
	const limit int64 = 1024
	smallBody := `{"key":"value"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(smallBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var called bool
	handler := RequestTransform(cfg, limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		body, _ := io.ReadAll(r.Body)
		var data map[string]any
		_ = json.Unmarshal(body, &data)
		if data["added"] != "val" {
			t.Error("injected field should be present")
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("next handler should have been called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestResponseTransform_BodySizeLimit(t *testing.T) {
	cfg := &config.DirectionTransform{
		Body: &config.BodyTransform{
			InjectFields: map[string]any{"added": "val"},
		},
	}
	const limit int64 = 64
	bigBody := `{"data":"` + strings.Repeat("x", int(limit)) + `"}`

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bigBody))
	})
	handler := ResponseTransform(cfg, limit)(upstream)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "bad_gateway" {
		t.Errorf("error = %q, want %q", resp["error"], "bad_gateway")
	}
}

func TestResponseTransform_BodyWithinLimit(t *testing.T) {
	cfg := &config.DirectionTransform{
		Body: &config.BodyTransform{
			InjectFields: map[string]any{"added": "val"},
		},
	}
	const limit int64 = 1024
	smallBody := `{"key":"value"}`

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(smallBody))
	})
	handler := ResponseTransform(cfg, limit)(upstream)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var data map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	if data["added"] != "val" {
		t.Error("injected field should be present")
	}
}

func TestBufferingResponseWriter_ExceedsLimit(t *testing.T) {
	buf := &bufferingResponseWriter{
		headers:    make(http.Header),
		statusCode: http.StatusOK,
		maxBody:    10,
	}
	// First write within limit.
	n, err := buf.Write([]byte("hello"))
	if n != 5 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (5, nil)", n, err)
	}
	if buf.exceeded {
		t.Error("should not be exceeded after first write")
	}
	// Second write exceeds limit.
	n, err = buf.Write([]byte("world!"))
	if n != 0 {
		t.Fatalf("second Write n = %d, want 0", n)
	}
	if err == nil {
		t.Fatal("second Write should return an error")
	}
	if !buf.exceeded {
		t.Error("should be exceeded after second write")
	}
	// Body should only contain the first write.
	if buf.body.String() != "hello" {
		t.Errorf("body = %q, want %q", buf.body.String(), "hello")
	}
}
