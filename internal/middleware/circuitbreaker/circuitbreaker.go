package circuitbreaker

import (
	"errors"
	"sync"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

// State represents the current state of a circuit breaker.
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

// String returns a human-readable label for the state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when the circuit breaker is in the open state
// and the timeout has not yet elapsed.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// OnStateChangeFunc is called when the circuit breaker transitions between states.
type OnStateChangeFunc func(routeName string, from, to State)

// Option configures a CircuitBreaker.
type Option func(*CircuitBreaker)

// WithOnStateChange sets a callback invoked on state transitions.
func WithOnStateChange(fn OnStateChangeFunc) Option {
	return func(cb *CircuitBreaker) { cb.onStateChange = fn }
}

// WithClock sets a custom time function for testing.
func WithClock(fn func() time.Time) Option {
	return func(cb *CircuitBreaker) { cb.now = fn }
}

// CircuitBreaker implements a thread-safe state machine that prevents
// cascading failures by short-circuiting requests when an upstream is unhealthy.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            State
	failureCount     int
	successCount     int
	failureThreshold int
	successThreshold int
	timeout          time.Duration
	lastFailureTime  time.Time
	routeName        string
	onStateChange    OnStateChangeFunc
	now              func() time.Time
}

// New creates a CircuitBreaker from the given configuration.
func New(cfg *config.CircuitBreakerConfig, routeName string, opts ...Option) *CircuitBreaker {
	cb := &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: cfg.FailureThreshold,
		successThreshold: cfg.SuccessThreshold,
		timeout:          cfg.Timeout,
		routeName:        routeName,
		now:              time.Now,
	}
	for _, o := range opts {
		o(cb)
	}
	return cb
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Execute runs fn through the circuit breaker. If the circuit is open and the
// timeout has not elapsed, ErrCircuitOpen is returned without calling fn.
// fn is executed outside the lock to avoid holding the mutex during slow calls.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()

	switch cb.state {
	case StateOpen:
		if cb.now().Sub(cb.lastFailureTime) >= cb.timeout {
			cb.setState(StateHalfOpen)
		} else {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
	}

	cb.mu.Unlock()

	// Execute outside the lock.
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failureCount++
		cb.successCount = 0
		cb.lastFailureTime = cb.now()
		if cb.state == StateHalfOpen {
			cb.setState(StateOpen)
		} else if cb.state == StateClosed && cb.failureCount >= cb.failureThreshold {
			cb.setState(StateOpen)
		}
	} else {
		cb.successCount++
		cb.failureCount = 0
		if cb.state == StateHalfOpen && cb.successCount >= cb.successThreshold {
			cb.setState(StateClosed)
		}
	}

	return err
}

// setState transitions the circuit breaker to the given state, resetting
// counters and invoking the state-change callback if set.
func (cb *CircuitBreaker) setState(to State) {
	from := cb.state
	cb.state = to
	cb.failureCount = 0
	cb.successCount = 0
	if cb.onStateChange != nil {
		cb.onStateChange(cb.routeName, from, to)
	}
}
