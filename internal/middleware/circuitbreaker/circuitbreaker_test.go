package circuitbreaker

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

func defaultCfg() *config.CircuitBreakerConfig {
	return &config.CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          5 * time.Second,
	}
}

func newTestCB(cfg *config.CircuitBreakerConfig, clock *time.Time, opts ...Option) *CircuitBreaker {
	allOpts := []Option{WithClock(func() time.Time { return *clock })}
	allOpts = append(allOpts, opts...)
	return New(cfg, "test-route", allOpts...)
}

func TestClosed_RequestsPassThrough(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

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
		t.Fatalf("expected StateClosed, got %v", cb.State())
	}
}

func TestClosed_ToOpen_AfterNFailures(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen after %d failures, got %v", 3, cb.State())
	}
}

func TestOpen_ImmediateReject(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	called := false
	err := cb.Execute(func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if called {
		t.Fatal("fn should not be called when circuit is open")
	}
}

func TestOpen_ToHalfOpen_AfterTimeout(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	now = now.Add(6 * time.Second)

	called := false
	cb.Execute(func() error {
		called = true
		return nil
	})

	if !called {
		t.Fatal("expected fn to be called in half-open state")
	}
}

func TestHalfOpen_ToClosed_AfterNSuccesses(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	now = now.Add(6 * time.Second)

	// successThreshold = 2
	for i := 0; i < 2; i++ {
		cb.Execute(func() error { return nil })
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %v", cb.State())
	}
}

func TestHalfOpen_ToOpen_OnFailure(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	now = now.Add(6 * time.Second)

	// One success in half-open, then fail
	cb.Execute(func() error { return nil })
	cb.Execute(func() error { return fail })

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen after failure in half-open, got %v", cb.State())
	}
}

func TestConcurrentAccess(t *testing.T) {
	now := time.Now()
	var mu sync.Mutex
	cb := New(defaultCfg(), "test-route",
		WithClock(func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}),
	)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cb.Execute(func() error {
				if n%3 == 0 {
					return errors.New("fail")
				}
				return nil
			})
		}(i)
	}
	wg.Wait()

	// No panic, no race — test succeeds
	s := cb.State()
	if s != StateClosed && s != StateOpen && s != StateHalfOpen {
		t.Fatalf("unexpected state %v", s)
	}
}

func TestOnStateChangeCallback(t *testing.T) {
	now := time.Now()

	type transition struct {
		route string
		from  State
		to    State
	}
	var transitions []transition

	cb := newTestCB(defaultCfg(), &now, WithOnStateChange(func(route string, from, to State) {
		transitions = append(transitions, transition{route, from, to})
	}))

	fail := errors.New("fail")
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(transitions))
	}
	if transitions[0].from != StateClosed || transitions[0].to != StateOpen {
		t.Fatalf("expected Closed→Open, got %v→%v", transitions[0].from, transitions[0].to)
	}
	if transitions[0].route != "test-route" {
		t.Fatalf("expected route 'test-route', got %q", transitions[0].route)
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
		{State(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", int(tt.state), got, tt.want)
		}
	}
}

func TestCountersResetOnSuccess(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	// 2 failures (below threshold of 3)
	cb.Execute(func() error { return fail })
	cb.Execute(func() error { return fail })
	// success resets failure count
	cb.Execute(func() error { return nil })
	// 2 more failures — still below threshold if reset worked
	cb.Execute(func() error { return fail })
	cb.Execute(func() error { return fail })

	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed (counters should have reset), got %v", cb.State())
	}
}

func TestCountersResetOnFailure(t *testing.T) {
	now := time.Now()
	cb := newTestCB(defaultCfg(), &now)

	fail := errors.New("fail")
	// Trip to open
	for i := 0; i < 3; i++ {
		cb.Execute(func() error { return fail })
	}

	// Advance past timeout → half-open
	now = now.Add(6 * time.Second)

	// 1 success in half-open (below successThreshold=2)
	cb.Execute(func() error { return nil })

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected StateHalfOpen after 1 success, got %v", cb.State())
	}

	// failure → back to open, counters reset
	cb.Execute(func() error { return fail })

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen after failure in half-open, got %v", cb.State())
	}

	// Advance past timeout again → half-open
	now = now.Add(6 * time.Second)

	// Need full successThreshold successes (counters were reset)
	for i := 0; i < 2; i++ {
		cb.Execute(func() error { return nil })
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed after full success threshold, got %v", cb.State())
	}
}
