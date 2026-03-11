package circuitbreaker

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

func newTestCB(failThresh, succThresh int, timeout time.Duration, opts ...Option) *CircuitBreaker {
	cfg := &config.CircuitBreakerConfig{
		FailureThreshold: failThresh,
		SuccessThreshold: succThresh,
		Timeout:          timeout,
	}
	return New(cfg, "test-route", opts...)
}

var errUpstream = errors.New("upstream error")

func TestClosed_RequestsPassThrough(t *testing.T) {
	cb := newTestCB(3, 1, time.Second)
	called := false
	err := cb.Execute(func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected fn to be called")
	}
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %s", cb.State())
	}
}

func TestClosed_ToOpen_AfterNFailures(t *testing.T) {
	cb := newTestCB(3, 1, time.Second)
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return errUpstream })
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}
}

func TestOpen_ImmediateReject(t *testing.T) {
	now := time.Now()
	cb := newTestCB(1, 1, time.Minute, WithClock(func() time.Time { return now }))
	cb.Execute(func() error { return errUpstream }) // trip to open

	called := false
	err := cb.Execute(func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if called {
		t.Fatal("fn should NOT have been called when circuit is open")
	}
}

func TestOpen_ToHalfOpen_AfterTimeout(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cb := newTestCB(1, 1, time.Second, WithClock(clock))
	cb.Execute(func() error { return errUpstream }) // trip to open

	// Advance clock past timeout
	now = now.Add(2 * time.Second)
	called := false
	cb.Execute(func() error {
		called = true
		return nil
	})
	if !called {
		t.Fatal("expected fn to be called in half-open")
	}
	// After success with successThreshold=1, should be back to closed
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed after half-open success, got %s", cb.State())
	}
}

func TestHalfOpen_ToClosed_AfterNSuccesses(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cb := newTestCB(1, 3, time.Second, WithClock(clock))
	cb.Execute(func() error { return errUpstream }) // open

	now = now.Add(2 * time.Second) // half-open
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return nil })
	}
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %s", cb.State())
	}
}

func TestHalfOpen_ToOpen_OnFailure(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cb := newTestCB(1, 3, time.Second, WithClock(clock))
	cb.Execute(func() error { return errUpstream }) // open

	now = now.Add(2 * time.Second) // half-open
	cb.Execute(func() error { return errUpstream })
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}
}

func TestConcurrentAccess(t *testing.T) {
	cb := newTestCB(50, 50, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				cb.Execute(func() error { return nil })
			} else {
				cb.Execute(func() error { return errUpstream })
			}
		}(i)
	}
	wg.Wait()
	// Just checking no races (run with -race)
}

func TestOnStateChangeCallback(t *testing.T) {
	var transitions []struct{ from, to State }
	cb := newTestCB(2, 1, time.Second, WithOnStateChange(func(route string, from, to State) {
		if route != "test-route" {
			t.Errorf("expected route 'test-route', got %q", route)
		}
		transitions = append(transitions, struct{ from, to State }{from, to})
	}))

	cb.Execute(func() error { return errUpstream })
	cb.Execute(func() error { return errUpstream }) // trip

	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(transitions))
	}
	if transitions[0].from != StateClosed || transitions[0].to != StateOpen {
		t.Fatalf("expected Closed→Open, got %s→%s", transitions[0].from, transitions[0].to)
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half_open"},
		{State(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestCountersResetOnSuccess(t *testing.T) {
	cb := newTestCB(3, 1, time.Second)
	// 2 failures (below threshold)
	cb.Execute(func() error { return errUpstream })
	cb.Execute(func() error { return errUpstream })
	// 1 success resets failure counter
	cb.Execute(func() error { return nil })
	// 2 more failures — should NOT trip (counter was reset)
	cb.Execute(func() error { return errUpstream })
	cb.Execute(func() error { return errUpstream })
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed (counter reset), got %s", cb.State())
	}
}

func TestCountersResetOnFailure(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cb := newTestCB(1, 3, time.Second, WithClock(clock))
	cb.Execute(func() error { return errUpstream }) // open

	now = now.Add(2 * time.Second)          // half-open
	cb.Execute(func() error { return nil }) // 1 success
	cb.Execute(func() error { return nil }) // 2 successes

	// Now a failure should send it back to open and reset success count
	now = now.Add(time.Millisecond)
	cb.Execute(func() error { return errUpstream })
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen after failure in half-open, got %s", cb.State())
	}
}

func TestExecuteFnCalledCount(t *testing.T) {
	cb := newTestCB(2, 1, time.Second)
	var count atomic.Int32
	fn := func() error {
		count.Add(1)
		return errUpstream
	}
	cb.Execute(fn) // 1 - closed
	cb.Execute(fn) // 2 - trips to open
	cb.Execute(fn) // 3 - should be rejected

	if count.Load() != 2 {
		t.Fatalf("expected fn called 2 times, got %d", count.Load())
	}
}
