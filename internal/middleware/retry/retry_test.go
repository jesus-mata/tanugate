package retry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/circuitbreaker"
)

// noSleep is a sleepFunc that never actually sleeps.
func noSleep(_ context.Context, _ time.Duration) bool { return false }

// fixedJitter returns a jitter function that always returns 1.0.
func fixedJitter() func() float64 { return func() float64 { return 1.0 } }

func newRetryCfg(maxRetries int, retryableCodes ...int) *config.RetryConfig {
	return &config.RetryConfig{
		MaxRetries:           maxRetries,
		InitialDelay:         10 * time.Millisecond,
		Multiplier:           2.0,
		RetryableStatusCodes: retryableCodes,
	}
}

func newCBCfg(failThresh, succThresh int, timeout time.Duration) *config.CircuitBreakerConfig {
	return &config.CircuitBreakerConfig{
		FailureThreshold: failThresh,
		SuccessThreshold: succThresh,
		Timeout:          timeout,
	}
}

// statusSequence returns a handler that returns the next status from codes on each call.
func statusSequence(codes ...int) http.Handler {
	var idx atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(idx.Add(1)) - 1
		code := codes[len(codes)-1] // default to last
		if i < len(codes) {
			code = codes[i]
		}
		w.WriteHeader(code)
	})
}

// countingHandler counts invocations and returns the given status.
func countingHandler(status int, count *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(status)
	})
}

func TestNoRetries_SingleAttempt(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		newRetryCfg(0, 503),
		nil,
		countingHandler(200, &count),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if count.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", count.Load())
	}
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRetryOnRetryableStatus(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		newRetryCfg(2, 503),
		nil,
		countingHandler(503, &count),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	// 1 initial + 2 retries = 3
	if count.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", count.Load())
	}
	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}

func TestSucceedsOnSecondAttempt(t *testing.T) {
	handler := Retry(
		newRetryCfg(2, 503),
		nil,
		statusSequence(503, 200),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAllRetriesExhausted_Returns502(t *testing.T) {
	handler := Retry(
		newRetryCfg(3, 503),
		nil,
		statusSequence(503, 503, 503, 503),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "bad_gateway" {
		t.Fatalf("expected error=bad_gateway, got %q", body["error"])
	}
}

func TestCircuitOpen_NoRetry_Immediate503(t *testing.T) {
	cb := circuitbreaker.New(newCBCfg(1, 1, time.Minute), "test")
	// Trip the circuit breaker
	cb.Execute(func() error { return io.ErrUnexpectedEOF })

	var count atomic.Int32
	handler := Retry(
		newRetryCfg(3, 503),
		cb,
		countingHandler(503, &count),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if count.Load() != 0 {
		t.Fatalf("upstream should not have been called, got %d calls", count.Load())
	}
	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "service_unavailable" {
		t.Fatalf("expected error=service_unavailable, got %q", body["error"])
	}
}

func TestCircuitOpens_MidRetry(t *testing.T) {
	// CB trips after 2 failures, but we allow 5 retries → should stop early at 503
	cb := circuitbreaker.New(newCBCfg(2, 1, time.Minute), "test")

	var count atomic.Int32
	handler := Retry(
		newRetryCfg(5, 503),
		cb,
		countingHandler(503, &count),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	// After 2 failures the CB trips, third attempt gets ErrCircuitOpen
	if count.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", count.Load())
	}
	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestBackoffDelaysIncrease(t *testing.T) {
	var delays []time.Duration
	sleepCapture := func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		return false
	}

	handler := Retry(
		newRetryCfg(3, 503),
		nil,
		statusSequence(503, 503, 503, 503),
		WithSleep(sleepCapture),
		WithJitter(fixedJitter()),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if len(delays) != 3 {
		t.Fatalf("expected 3 delays, got %d", len(delays))
	}

	// With jitter=1.0, multiplier=2.0, initial=10ms: 10ms, 20ms, 40ms
	expected := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	for i, want := range expected {
		if delays[i] != want {
			t.Errorf("delay[%d] = %v, want %v", i, delays[i], want)
		}
	}
}

func TestJitterRange(t *testing.T) {
	var delays []time.Duration
	callCount := 0
	jitterValues := []float64{0.5, 1.0, 1.49}
	jitterFn := func() float64 {
		v := jitterValues[callCount%len(jitterValues)]
		callCount++
		return v
	}

	sleepCapture := func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		return false
	}

	handler := Retry(
		newRetryCfg(3, 503),
		nil,
		statusSequence(503, 503, 503, 503),
		WithSleep(sleepCapture),
		WithJitter(jitterFn),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	// Verify that delays scale with jitter:
	// attempt 1: 10ms * 0.5 = 5ms
	// attempt 2: 20ms * 1.0 = 20ms
	// attempt 3: 40ms * 1.49 = 59.6ms
	if delays[0] != 5*time.Millisecond {
		t.Errorf("delay[0] = %v, want 5ms", delays[0])
	}
	if delays[1] != 20*time.Millisecond {
		t.Errorf("delay[1] = %v, want 20ms", delays[1])
	}
	// 40ms * 1.49 = 59.6ms → truncated to 59ms by Duration
	if delays[2] < 59*time.Millisecond || delays[2] > 60*time.Millisecond {
		t.Errorf("delay[2] = %v, want ~59.6ms", delays[2])
	}
}

func TestRequestBodyReReadable(t *testing.T) {
	var bodies []string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) < 3 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	})

	handler := Retry(
		newRetryCfg(3, 503),
		nil,
		upstream,
		WithSleep(noSleep),
	)

	body := `{"key":"value"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for i, b := range bodies {
		if b != body {
			t.Errorf("attempt %d body = %q, want %q", i, b, body)
		}
	}
}

func TestNonRetryableStatus_NotRetried(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		newRetryCfg(3, 503),
		nil,
		countingHandler(400, &count),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if count.Load() != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", count.Load())
	}
	if rec.Code != 400 {
		t.Fatalf("expected 400 pass-through, got %d", rec.Code)
	}
}

func TestNoCircuitBreaker_RetryOnly(t *testing.T) {
	handler := Retry(
		newRetryCfg(2, 503),
		nil, // no CB
		statusSequence(503, 503, 200),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestCBOnly_NoRetry(t *testing.T) {
	cb := circuitbreaker.New(newCBCfg(2, 1, time.Minute), "test")
	var count atomic.Int32

	handler := WithCircuitBreaker(cb, countingHandler(500, &count))

	// First two calls trip the CB
	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 500 {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	// Third call should be rejected
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("expected 503 (circuit open), got %d", rec.Code)
	}
	if count.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", count.Load())
	}
}

func TestResponseHeadersCopied(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "hello")
		w.WriteHeader(200)
	})

	handler := Retry(
		newRetryCfg(1, 503),
		nil,
		upstream,
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Header().Get("X-Custom") != "hello" {
		t.Fatalf("expected X-Custom=hello, got %q", rec.Header().Get("X-Custom"))
	}
}

func TestEmptyBody(t *testing.T) {
	handler := Retry(
		newRetryCfg(2, 503),
		nil,
		statusSequence(503, 200),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestLargeBody(t *testing.T) {
	largeBody := bytes.Repeat([]byte("A"), 1024*1024) // 1MB
	var lastBody []byte

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = b
		if len(lastBody) != len(largeBody) {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})

	handler := Retry(
		newRetryCfg(2, 503),
		nil,
		upstream,
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", bytes.NewReader(largeBody))
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Equal(lastBody, largeBody) {
		t.Fatal("large body not correctly re-sent")
	}
}

func TestIntegration_FailThenRecover(t *testing.T) {
	cb := circuitbreaker.New(newCBCfg(5, 1, time.Second), "test")
	handler := Retry(
		newRetryCfg(4, 503),
		cb,
		statusSequence(503, 503, 503, 200),
		WithSleep(noSleep),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestIntegration_CircuitTripsAndRecovers(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cb := circuitbreaker.New(
		newCBCfg(2, 1, 100*time.Millisecond),
		"test",
		circuitbreaker.WithClock(clock),
	)

	var reqCount atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		if n <= 2 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	})

	handler := Retry(
		newRetryCfg(5, 503),
		cb,
		upstream,
		WithSleep(noSleep),
	)

	// First request: 2 failures trip CB, then ErrCircuitOpen → 503
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	// Advance past timeout → half-open
	now = now.Add(200 * time.Millisecond)

	// Second request: should succeed (upstream now healthy)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	if rec2.Code != 200 {
		t.Fatalf("expected 200 after recovery, got %d", rec2.Code)
	}

	if cb.State() != circuitbreaker.StateClosed {
		t.Fatalf("expected circuit to be closed after recovery, got %s", cb.State())
	}
}
