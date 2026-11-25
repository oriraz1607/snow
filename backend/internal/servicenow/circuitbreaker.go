package servicenow

import (
	"sync"
	"time"
)

type BreakerState int

const (
	BreakerClosed   BreakerState = 0
	BreakerOpen     BreakerState = 1
	BreakerHalfOpen BreakerState = 2
)

const (
	defaultFailureThreshold = 5
	defaultFailureWindow    = 60 * time.Second
	defaultOpenDuration     = 30 * time.Second
)

// CircuitBreaker guards outbound ServiceNow calls against prolonged outages:
// >= 5 consecutive 5xx/timeout errors within 60s opens for 30s, followed by a half-open probe.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            BreakerState
	consecutiveFails int
	firstFailAt      time.Time
	openedAt         time.Time
	halfOpenInFlight bool

	failureThreshold int
	failureWindow    time.Duration
	openDuration     time.Duration
	now              func() time.Time
	onStateChange    func(BreakerState)
}

// NewCircuitBreaker constructs a CircuitBreaker with specification defaults.
func NewCircuitBreaker(onStateChange func(BreakerState)) *CircuitBreaker {
	return &CircuitBreaker{
		state:            BreakerClosed,
		failureThreshold: defaultFailureThreshold,
		failureWindow:    defaultFailureWindow,
		openDuration:     defaultOpenDuration,
		now:              time.Now,
		onStateChange:    onStateChange,
	}
}

// Allow reports whether a request may proceed. If the circuit is half-open, only one
// concurrent probe is permitted until RecordSuccess or RecordFailure settles it.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.now()
	switch cb.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if now.Sub(cb.openedAt) >= cb.openDuration {
			cb.setStateLocked(BreakerHalfOpen)
			cb.halfOpenInFlight = true
			return true
		}
		return false
	case BreakerHalfOpen:
		if cb.halfOpenInFlight {
			return false
		}
		cb.halfOpenInFlight = true
		return true
	default:
		return true
	}
}

// State returns the current BreakerState, advancing Open -> HalfOpen if cooldown elapsed.
func (cb *CircuitBreaker) State() BreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == BreakerOpen && cb.now().Sub(cb.openedAt) >= cb.openDuration {
		cb.setStateLocked(BreakerHalfOpen)
	}
	return cb.state
}

// RecordSuccess closes the circuit and resets consecutive failure counters.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFails = 0
	cb.firstFailAt = time.Time{}
	cb.halfOpenInFlight = false
	if cb.state != BreakerClosed {
		cb.setStateLocked(BreakerClosed)
	}
}

// RecordFailure records an upstream 5xx/timeout/transport failure.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.now()
	cb.halfOpenInFlight = false

	if cb.state == BreakerHalfOpen {
		cb.openedAt = now
		cb.setStateLocked(BreakerOpen)
		return
	}

	if cb.consecutiveFails == 0 || now.Sub(cb.firstFailAt) > cb.failureWindow {
		cb.firstFailAt = now
		cb.consecutiveFails = 1
	} else {
		cb.consecutiveFails++
	}

	if cb.consecutiveFails >= cb.failureThreshold {
		cb.openedAt = now
		cb.consecutiveFails = 0
		cb.firstFailAt = time.Time{}
		cb.setStateLocked(BreakerOpen)
	}
}

func (cb *CircuitBreaker) setStateLocked(next BreakerState) {
	cb.state = next
	if cb.onStateChange != nil {
		cb.onStateChange(next)
	}
}
