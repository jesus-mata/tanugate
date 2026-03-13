package retry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware/circuitbreaker"
)

const maxRetryBodySize = 10 << 20 // 10 MB

// sleepFunc is the function used to pause between retries. It can be replaced
// in tests via WithSleep to avoid real delays.
type sleepFunc func(time.Duration)

// Option configures optional behaviour on a retry handler.
type Option func(*retryHandler)

// WithSleep overrides the sleep function used between retries.
func WithSleep(fn sleepFunc) Option {
	return func(h *retryHandler) {
		h.sleep = fn
	}
}

type retryHandler struct {
	cfg   *config.RetryConfig
	cb    *circuitbreaker.CircuitBreaker
	proxy http.Handler
	sleep sleepFunc

	retryable map[int]bool
}

// Retry returns an http.Handler that retries failed upstream requests with
// exponential backoff and jitter. If cb is non-nil, each attempt is executed
// through the circuit breaker.
func Retry(cfg *config.RetryConfig, cb *circuitbreaker.CircuitBreaker, proxy http.Handler, opts ...Option) http.Handler {
	h := &retryHandler{
		cfg:   cfg,
		cb:    cb,
		proxy: proxy,
		sleep: time.Sleep,
	}
	for _, opt := range opts {
		opt(h)
	}

	h.retryable = make(map[int]bool, len(cfg.RetryableStatusCodes))
	for _, code := range cfg.RetryableStatusCodes {
		h.retryable[code] = true
	}

	return h
}

// WithCircuitBreaker returns an http.Handler that wraps proxy calls in a
// circuit breaker without retry logic.
func WithCircuitBreaker(cb *circuitbreaker.CircuitBreaker, proxy http.Handler) http.Handler {
	return &cbOnlyHandler{cb: cb, proxy: proxy}
}

type cbOnlyHandler struct {
	cb    *circuitbreaker.CircuitBreaker
	proxy http.Handler
}

func (h *cbOnlyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	err := h.cb.Execute(func() error {
		h.proxy.ServeHTTP(rec, r)
		if rec.Code >= 500 {
			return fmt.Errorf("upstream returned %d", rec.Code)
		}
		return nil
	})

	if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		writeJSON(w, http.StatusServiceUnavailable, "service_unavailable", "circuit breaker is open")
		return
	}

	copyRecorderToWriter(rec, w)
}

func (h *retryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Buffer request body for replay.
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, maxRetryBodySize+1))
		r.Body.Close()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, "bad_gateway", "failed to read request body")
			return
		}
		if len(bodyBytes) > maxRetryBodySize {
			writeJSON(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 10MB retry buffer limit")
			return
		}
	}

	var lastErr error
	maxAttempts := h.cfg.MaxRetries + 1

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := h.backoff(attempt)
			select {
			case <-r.Context().Done():
				writeJSON(w, 499, "client_closed", "client disconnected during retry")
				return
			default:
			}
			h.sleep(delay)
			// Check again after sleep.
			select {
			case <-r.Context().Done():
				writeJSON(w, 499, "client_closed", "client disconnected during retry")
				return
			default:
			}
		}

		// Clone request with fresh body.
		clone := r.Clone(r.Context())
		if bodyBytes != nil {
			clone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			clone.ContentLength = int64(len(bodyBytes))
		}

		rec := httptest.NewRecorder()

		var execErr error
		if h.cb != nil {
			execErr = h.cb.Execute(func() error {
				rec = httptest.NewRecorder()
				h.proxy.ServeHTTP(rec, clone)
				if rec.Code >= 500 {
					return fmt.Errorf("upstream returned %d", rec.Code)
				}
				return nil
			})
		} else {
			h.proxy.ServeHTTP(rec, clone)
		}

		if errors.Is(execErr, circuitbreaker.ErrCircuitOpen) {
			writeJSON(w, http.StatusServiceUnavailable, "service_unavailable", "circuit breaker is open")
			return
		}

		if execErr != nil {
			lastErr = execErr
			continue
		}

		// Check if response status is retryable.
		if h.retryable[rec.Code] {
			lastErr = fmt.Errorf("upstream returned %d", rec.Code)
			continue
		}

		// Success — copy response.
		copyRecorderToWriter(rec, w)
		return
	}

	// All retries exhausted.
	msg := fmt.Sprintf("upstream failed after %d attempts", maxAttempts)
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeJSON(w, http.StatusBadGateway, "bad_gateway", msg)
}

func (h *retryHandler) backoff(attempt int) time.Duration {
	delay := float64(h.cfg.InitialDelay) * math.Pow(h.cfg.Multiplier, float64(attempt-1))
	jitter := 0.5 + rand.Float64() // [0.5, 1.5)
	return time.Duration(delay * jitter)
}

func copyRecorderToWriter(rec *httptest.ResponseRecorder, w http.ResponseWriter) {
	for k, vals := range rec.Header() {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

func writeJSON(w http.ResponseWriter, status int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   errCode,
		"message": message,
	})
}
