package retry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/circuitbreaker"
)

// sleepFunc sleeps for the given duration respecting context cancellation.
// Returns true if the context was cancelled.
type sleepFunc func(ctx context.Context, d time.Duration) (cancelled bool)

// options holds internal retry configuration used for testing hooks.
type options struct {
	sleep    sleepFunc
	jitterFn func() float64 // returns a value in [0.5, 1.5)
}

// Option configures retry behaviour (used for testing).
type Option func(*options)

// WithSleep overrides the sleep function (for testing).
func WithSleep(fn sleepFunc) Option {
	return func(o *options) { o.sleep = fn }
}

// WithJitter overrides the jitter function (for testing).
func WithJitter(fn func() float64) Option {
	return func(o *options) { o.jitterFn = fn }
}

func defaultSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return false
	case <-ctx.Done():
		return true
	}
}

// Retry returns an http.Handler that retries requests through the given proxy
// handler according to cfg. If cb is non-nil, requests are executed through
// the circuit breaker.
func Retry(cfg *config.RetryConfig, cb *circuitbreaker.CircuitBreaker, proxy http.Handler, opts ...Option) http.Handler {
	retryable := make(map[int]bool, len(cfg.RetryableStatusCodes))
	for _, code := range cfg.RetryableStatusCodes {
		retryable[code] = true
	}

	o := &options{
		sleep:    defaultSleep,
		jitterFn: func() float64 { return 0.5 + rand.Float64() },
	}
	for _, opt := range opts {
		opt(o)
	}

	multiplier := cfg.Multiplier
	if multiplier == 0 {
		multiplier = 2.0
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffer request body for replaying.
		var bodyBytes []byte
		if r.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				writeJSON(w, http.StatusBadGateway, "bad_gateway", "failed to read request body")
				return
			}
		}

		var lastErr error
		var lastRecorder *httptest.ResponseRecorder

		for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
			if attempt > 0 {
				delay := cfg.InitialDelay * time.Duration(math.Pow(multiplier, float64(attempt-1)))
				jitter := o.jitterFn()
				sleepDuration := time.Duration(float64(delay) * jitter)

				if o.sleep(r.Context(), sleepDuration) {
					writeJSON(w, 499, "client_closed", "client disconnected during retry")
					return
				}
			}

			// Check context cancellation before attempt.
			select {
			case <-r.Context().Done():
				writeJSON(w, 499, "client_closed", "client disconnected during retry")
				return
			default:
			}

			// Clone request and reset body.
			cloned := r.Clone(r.Context())
			if bodyBytes != nil {
				cloned.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				cloned.ContentLength = int64(len(bodyBytes))
			}

			rec := httptest.NewRecorder()

			var execErr error
			if cb != nil {
				execErr = cb.Execute(func() error {
					rec2 := httptest.NewRecorder()
					proxy.ServeHTTP(rec2, cloned)
					copyRecorder(rec, rec2)
					if retryable[rec2.Code] {
						return fmt.Errorf("retryable status %d", rec2.Code)
					}
					return nil
				})
			} else {
				proxy.ServeHTTP(rec, cloned)
				if retryable[rec.Code] {
					execErr = fmt.Errorf("retryable status %d", rec.Code)
				}
			}

			if execErr != nil {
				if execErr == circuitbreaker.ErrCircuitOpen {
					writeJSON(w, http.StatusServiceUnavailable, "service_unavailable", "circuit breaker is open")
					return
				}
				lastErr = execErr
				lastRecorder = rec
				continue
			}

			// Success — copy captured response to real writer.
			copyToResponse(w, rec)
			return
		}

		// All retries exhausted.
		if lastRecorder != nil && !retryable[lastRecorder.Code] {
			copyToResponse(w, lastRecorder)
			return
		}

		msg := fmt.Sprintf("upstream failed after %d attempts", cfg.MaxRetries+1)
		if lastErr != nil {
			msg = fmt.Sprintf("upstream failed after %d attempts: %v", cfg.MaxRetries+1, lastErr)
		}
		writeJSON(w, http.StatusBadGateway, "bad_gateway", msg)
	})
}

// WithCircuitBreaker wraps a proxy handler with circuit breaker protection
// only (no retry logic).
func WithCircuitBreaker(cb *circuitbreaker.CircuitBreaker, proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		err := cb.Execute(func() error {
			proxy.ServeHTTP(rec, r)
			if rec.Code >= 500 {
				return fmt.Errorf("upstream returned %d", rec.Code)
			}
			return nil
		})

		if err == circuitbreaker.ErrCircuitOpen {
			writeJSON(w, http.StatusServiceUnavailable, "service_unavailable", "circuit breaker is open")
			return
		}

		copyToResponse(w, rec)
	})
}

func writeJSON(w http.ResponseWriter, status int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   errCode,
		"message": message,
	})
}

func copyToResponse(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

func copyRecorder(dst, src *httptest.ResponseRecorder) {
	dst.Code = src.Code
	dst.Body = src.Body
	for k, v := range src.Header() {
		dst.Header()[k] = v
	}
}
