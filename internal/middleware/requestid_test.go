package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestRequestID_Generated(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RequestID()(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	id := rr.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("X-Request-ID header is empty")
	}

	if !uuidV4Regex.MatchString(id) {
		t.Errorf("X-Request-ID %q does not match UUID v4 format", id)
	}
}

func TestRequestID_Propagated(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RequestID()(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "my-custom-id")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	id := rr.Header().Get("X-Request-ID")
	if id != "my-custom-id" {
		t.Errorf("X-Request-ID = %q, want %q", id, "my-custom-id")
	}
}

func TestRequestID_InContext(t *testing.T) {
	var ctxID string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RequestID()(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "ctx-test-id")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if ctxID != "ctx-test-id" {
		t.Errorf("RequestIDFromContext() = %q, want %q", ctxID, "ctx-test-id")
	}

	responseID := rr.Header().Get("X-Request-ID")
	if responseID != ctxID {
		t.Errorf("response X-Request-ID = %q, context ID = %q, want them equal", responseID, ctxID)
	}
}

func TestRequestID_Uniqueness(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RequestID()(handler)

	seen := make(map[string]struct{}, 1000)

	for i := range 1000 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		id := rr.Header().Get("X-Request-ID")
		if id == "" {
			t.Fatalf("iteration %d: X-Request-ID header is empty", i)
		}

		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request ID found: %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	id := RequestIDFromContext(context.Background())
	if id != "" {
		t.Errorf("RequestIDFromContext(context.Background()) = %q, want %q", id, "")
	}
}
