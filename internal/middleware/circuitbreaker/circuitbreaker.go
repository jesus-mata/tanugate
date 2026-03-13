package circuitbreaker

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
)

// State represents the current state of a circuit breaker.
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

// String returns a human-readable representation of the State.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// ErrCircuitOpen is returned when a request is rejected because the circuit
// breaker is in the Open state and the timeout has not yet elapsed.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// OnStateChangeFunc is called whenever the circuit breaker transitions between
// states. It receives the route name and the old and new states.
type OnStateChangeFunc func(routeName string, from, to State)

// Option configures optional behaviour on a CircuitBreaker.
type Option func(*CircuitBreaker)

// WithOnStateChange registers a callback that fires on every state transition.
func WithOnStateChange(fn OnStateChangeFunc) Option {
	return func(cb *CircuitBreaker) {
		cb.onStateChange = fn
	}
}

// WithClock overrides the function used to obtain the current time. This is
// useful in tests to advance time without sleeping.
func WithClock(fn func() time.Time) Option {
	return func(cb *CircuitBreaker) {
		cb.now = fn
	}
}

// CircuitBreaker implements a three-state circuit breaker (Closed → Open →
// HalfOpen → Closed) that wraps upstream calls. In HalfOpen state, only a
// single probe request is allowed through; all others are rejected with
// ErrCircuitOpen until the probe completes.
type CircuitBreaker struct {
	mu sync.Mutex

	state           State
	failureCount    int
	successCount    int
	halfOpenProbing bool // true while a probe request is in-flight during HalfOpen

	failureThreshold int
	successThreshold int
	timeout          time.Duration

	openedAt time.Time

	routeName     string
	onStateChange OnStateChangeFunc
	now           func() time.Time
}

// New creates a CircuitBreaker with the given configuration and options.
func New(cfg *config.CircuitBreakerConfig, routeName string, opts ...Option) *CircuitBreaker {
	cb := &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: cfg.FailureThreshold,
		successThreshold: cfg.SuccessThreshold,
		timeout:          cfg.Timeout,
		routeName:        routeName,
		now:              time.Now,
	}
	for _, opt := range opts {
		opt(cb)
	}
	return cb
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Execute runs fn through the circuit breaker. It returns ErrCircuitOpen when
// the breaker is open and the timeout has not elapsed. The function fn is
// executed outside the lock to avoid holding the mutex during slow calls.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()

	// Section A: pre-fn — check whether we can proceed.
	var preFrom State
	var preChanged bool

	switch cb.state {
	case StateOpen:
		if cb.now().Sub(cb.openedAt) >= cb.timeout {
			preFrom, preChanged = cb.setState(StateHalfOpen)
		} else {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
	}

	// In HalfOpen, only one probe request is allowed at a time.
	isProbe := false
	if cb.state == StateHalfOpen {
		if cb.halfOpenProbing {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
		cb.halfOpenProbing = true
		isProbe = true
	}

	cb.mu.Unlock()

	// Fire pre-fn callback (Open→HalfOpen) outside the lock.
	if preChanged && cb.onStateChange != nil {
		cb.onStateChange(cb.routeName, preFrom, StateHalfOpen)
	}

	// Execute fn outside the lock.
	err := fn()

	// Section B: post-fn — record the outcome and possibly transition.
	cb.mu.Lock()

	if isProbe {
		cb.halfOpenProbing = false
	}

	var postFrom State
	var postTo State
	var postChanged bool

	if err != nil {
		cb.failureCount++
		cb.successCount = 0
		if cb.state == StateHalfOpen {
			postFrom, postChanged = cb.setState(StateOpen)
			postTo = StateOpen
		} else if cb.state == StateClosed && cb.failureCount >= cb.failureThreshold {
			postFrom, postChanged = cb.setState(StateOpen)
			postTo = StateOpen
		}
	} else {
		cb.successCount++
		cb.failureCount = 0
		if cb.state == StateHalfOpen && cb.successCount >= cb.successThreshold {
			postFrom, postChanged = cb.setState(StateClosed)
			postTo = StateClosed
		}
	}

	cb.mu.Unlock()

	// Fire post-fn callback outside the lock.
	if postChanged && cb.onStateChange != nil {
		cb.onStateChange(cb.routeName, postFrom, postTo)
	}

	return err
}

// setState transitions the circuit breaker to a new state. It returns the
// previous state and whether a transition actually occurred so the caller can
// fire the onStateChange callback after releasing the mutex.
// Must be called with mu held.
func (cb *CircuitBreaker) setState(to State) (from State, changed bool) {
	from = cb.state
	cb.state = to
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenProbing = false

	if to == StateOpen {
		cb.openedAt = cb.now()
	}

	return from, from != to
}
