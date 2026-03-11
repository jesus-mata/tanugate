package retry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func retryCfg(maxRetries int, codes ...int) *config.RetryConfig {
	return &config.RetryConfig{
		MaxRetries:           maxRetries,
		InitialDelay:         100 * time.Millisecond,
		Multiplier:           2.0,
		RetryableStatusCodes: codes,
	}
}

func cbCfg(failThreshold int) *config.CircuitBreakerConfig {
	return &config.CircuitBreakerConfig{
		FailureThreshold: failThreshold,
		SuccessThreshold: 1,
		Timeout:          5 * time.Second,
	}
}

func noSleep() Option {
	return WithSleep(func(d time.Duration) {})
}

func countingHandler(counter *atomic.Int32, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		w.WriteHeader(status)
	})
}

// sequenceHandler returns different status codes for sequential requests.
func sequenceHandler(statuses []int, counter *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(counter.Add(1)) - 1
		status := statuses[len(statuses)-1]
		if idx < len(statuses) {
			status = statuses[idx]
		}
		w.WriteHeader(status)
	})
}

func TestNoRetries_SingleAttempt(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(0, 503),
		nil,
		countingHandler(&count, 200),
		noSleep(),
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
		retryCfg(2, 503),
		nil,
		countingHandler(&count, 503),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if count.Load() != 3 {
		t.Fatalf("expected 3 calls (1 + 2 retries), got %d", count.Load())
	}
}

func TestSucceedsOnSecondAttempt(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(2, 503),
		nil,
		sequenceHandler([]int{503, 200}, &count),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if count.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", count.Load())
	}
}

func TestAllRetriesExhausted_Returns502(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(2, 503),
		nil,
		countingHandler(&count, 503),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "bad_gateway" {
		t.Fatalf("expected bad_gateway error, got %q", body["error"])
	}
}

func TestCircuitOpen_NoRetry_Immediate503(t *testing.T) {
	now := time.Now()
	cb := circuitbreaker.New(cbCfg(1), "test",
		circuitbreaker.WithClock(func() time.Time { return now }),
	)
	// Trip the circuit.
	cb.Execute(func() error { return errors.New("fail") })

	var count atomic.Int32
	handler := Retry(
		retryCfg(3, 503),
		cb,
		countingHandler(&count, 200),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if count.Load() != 0 {
		t.Fatalf("expected 0 upstream calls, got %d", count.Load())
	}
}

func TestCircuitOpens_MidRetry(t *testing.T) {
	now := time.Now()
	cb := circuitbreaker.New(cbCfg(2), "test",
		circuitbreaker.WithClock(func() time.Time { return now }),
	)

	var count atomic.Int32
	// Always return 500 so CB records failures
	handler := Retry(
		retryCfg(5, 500),
		cb,
		countingHandler(&count, 500),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 503 {
		t.Fatalf("expected 503 (circuit open), got %d", rec.Code)
	}
	// CB trips after 2 failures, so attempt 3 should get ErrCircuitOpen
	if count.Load() != 2 {
		t.Fatalf("expected 2 upstream calls before CB tripped, got %d", count.Load())
	}
}

func TestBackoffDelaysIncrease(t *testing.T) {
	var delays []time.Duration
	sleepCapture := func(d time.Duration) {
		delays = append(delays, d)
	}

	var count atomic.Int32
	handler := Retry(
		retryCfg(4, 503),
		nil,
		countingHandler(&count, 503),
		WithSleep(sleepCapture),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if len(delays) != 4 {
		t.Fatalf("expected 4 delays, got %d", len(delays))
	}

	// Each delay should be greater than the previous (statistically, with jitter)
	// but we can at least verify the base pattern: each delay should be at least
	// half the nominal value and the nominal value doubles.
	cfg := retryCfg(4, 503)
	for i, d := range delays {
		nominal := float64(cfg.InitialDelay) * pow(cfg.Multiplier, i)
		minExpected := time.Duration(nominal * 0.5)
		maxExpected := time.Duration(nominal * 1.5)
		if d < minExpected || d >= maxExpected {
			t.Errorf("delay[%d] = %v, expected [%v, %v)", i, d, minExpected, maxExpected)
		}
	}
}

func pow(base float64, exp int) float64 {
	result := 1.0
	for range exp {
		result *= base
	}
	return result
}

func TestJitterRange(t *testing.T) {
	var delays []time.Duration
	sleepCapture := func(d time.Duration) {
		delays = append(delays, d)
	}

	var count atomic.Int32
	// Run many retries to get a statistical sample.
	cfg := &config.RetryConfig{
		MaxRetries:           20,
		InitialDelay:         100 * time.Millisecond,
		Multiplier:           1.0, // constant base for easy verification
		RetryableStatusCodes: []int{503},
	}

	handler := Retry(cfg, nil, countingHandler(&count, 503), WithSleep(sleepCapture))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	for i, d := range delays {
		min := time.Duration(float64(cfg.InitialDelay) * 0.5)
		max := time.Duration(float64(cfg.InitialDelay) * 1.5)
		if d < min || d >= max {
			t.Errorf("delay[%d] = %v, outside jitter range [%v, %v)", i, d, min, max)
		}
	}
}

func TestRequestBodyReReadable(t *testing.T) {
	var bodies []string
	var count atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if int(count.Load()) < 3 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	})

	handler := Retry(retryCfg(3, 503), nil, upstream, noSleep())
	body := `{"key":"value"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for i, b := range bodies {
		if b != body {
			t.Errorf("attempt %d: body = %q, want %q", i, b, body)
		}
	}
}

func TestNonRetryableStatus_NotRetried(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(3, 503),
		nil,
		countingHandler(&count, 400),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if count.Load() != 1 {
		t.Fatalf("expected 1 call (no retry for 400), got %d", count.Load())
	}
}

func TestNoCircuitBreaker_RetryOnly(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(2, 503),
		nil,
		sequenceHandler([]int{503, 503, 200}, &count),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if count.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", count.Load())
	}
}

func TestCBOnly_NoRetry(t *testing.T) {
	now := time.Now()
	cb := circuitbreaker.New(cbCfg(2), "test",
		circuitbreaker.WithClock(func() time.Time { return now }),
	)

	var count atomic.Int32
	handler := WithCircuitBreaker(cb, countingHandler(&count, 500))

	// Two 500s should trip the CB.
	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}

	// Third request should get 503 (circuit open).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if count.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", count.Load())
	}
}

func TestResponseHeadersCopied(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "test-value")
		w.WriteHeader(200)
	})

	handler := Retry(retryCfg(1, 503), nil, upstream, noSleep())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if got := rec.Header().Get("X-Custom"); got != "test-value" {
		t.Fatalf("expected X-Custom=test-value, got %q", got)
	}
}

func TestEmptyBody(t *testing.T) {
	var count atomic.Int32
	handler := Retry(
		retryCfg(2, 503),
		nil,
		sequenceHandler([]int{503, 200}, &count),
		noSleep(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestLargeBody(t *testing.T) {
	largeBody := bytes.Repeat([]byte("A"), 1<<20) // 1MB

	var receivedSizes []int
	var count atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		b, _ := io.ReadAll(r.Body)
		receivedSizes = append(receivedSizes, len(b))
		if int(count.Load()) < 2 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	})

	handler := Retry(retryCfg(2, 503), nil, upstream, noSleep())
	req := httptest.NewRequest("POST", "/", bytes.NewReader(largeBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for i, size := range receivedSizes {
		if size != len(largeBody) {
			t.Errorf("attempt %d: received %d bytes, want %d", i, size, len(largeBody))
		}
	}
}

// Integration test: failures then recovery
func TestIntegration_FailThenRecover(t *testing.T) {
	var count atomic.Int32
	upstream := sequenceHandler([]int{503, 503, 503, 200}, &count)

	now := time.Now()
	cb := circuitbreaker.New(
		&config.CircuitBreakerConfig{
			FailureThreshold: 5,
			SuccessThreshold: 1,
			Timeout:          5 * time.Second,
		},
		"test",
		circuitbreaker.WithClock(func() time.Time { return now }),
	)

	handler := Retry(retryCfg(4, 503), cb, upstream, noSleep())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if count.Load() != 4 {
		t.Fatalf("expected 4 upstream calls, got %d", count.Load())
	}
}

// Integration test: circuit trips and recovers
func TestIntegration_CircuitTripsAndRecovers(t *testing.T) {
	now := time.Now()
	cb := circuitbreaker.New(
		&config.CircuitBreakerConfig{
			FailureThreshold: 2,
			SuccessThreshold: 1,
			Timeout:          5 * time.Second,
		},
		"test",
		circuitbreaker.WithClock(func() time.Time { return now }),
	)

	var count atomic.Int32
	failHandler := countingHandler(&count, 500)

	// Two failures trip the CB.
	for range 2 {
		rec := httptest.NewRecorder()
		handler := Retry(retryCfg(0, 503), cb, failHandler, noSleep())
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}

	// CB is open — immediate 503.
	rec := httptest.NewRecorder()
	handler := Retry(retryCfg(0, 503), cb, failHandler, noSleep())
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("expected 503 (circuit open), got %d", rec.Code)
	}

	// Advance time past timeout → half-open.
	now = now.Add(6 * time.Second)

	var successCount atomic.Int32
	successHandler := countingHandler(&successCount, 200)
	rec = httptest.NewRecorder()
	handler = Retry(retryCfg(0, 503), cb, successHandler, noSleep())
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200 after recovery, got %d", rec.Code)
	}

	// CB should be closed now.
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	cancel()
}
