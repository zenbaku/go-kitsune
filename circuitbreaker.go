package kitsune

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

// CircuitState is the state of a circuit breaker stage.
type CircuitState int32

const (
	// CircuitClosed is normal operation: items pass through and failures are counted.
	CircuitClosed CircuitState = iota
	// CircuitOpen means too many consecutive failures occurred: all items are
	// rejected with [ErrCircuitOpen] until the cooldown expires.
	CircuitOpen
	// CircuitHalfOpen allows a limited number of probe items through after the
	// cooldown. A successful probe closes the circuit; a failed probe re-opens it.
	CircuitHalfOpen
)

// ErrCircuitOpen is returned when a [CircuitBreaker] stage rejects an item
// because the circuit is open. Combine with [OnError](Skip()) to silently
// discard rejected items.
var ErrCircuitOpen = errors.New("kitsune: circuit breaker open")

// CircuitBreakerOption configures a [CircuitBreaker] stage.
type CircuitBreakerOption func(*cbConfig)

type cbConfig struct {
	failureThreshold int
	cooldown         time.Duration
	halfOpenProbes   int
}

// FailureThreshold sets the number of consecutive failures that trip the
// circuit breaker into the open state. Defaults to 5.
func FailureThreshold(n int) CircuitBreakerOption {
	return func(c *cbConfig) { c.failureThreshold = n }
}

// CooldownDuration sets the time the circuit stays open before transitioning
// to half-open to test recovery. Defaults to 30s.
func CooldownDuration(d time.Duration) CircuitBreakerOption {
	return func(c *cbConfig) { c.cooldown = d }
}

// HalfOpenProbes sets the maximum number of probe items allowed through in
// the half-open state before a decision is made. Defaults to 1.
func HalfOpenProbes(n int) CircuitBreakerOption {
	return func(c *cbConfig) { c.halfOpenProbes = n }
}

// cbState is the internal state for one circuit breaker instance,
// stored in a [Ref] via [MapWith].
type cbState struct {
	State            CircuitState
	ConsecutiveFails int
	LastFailTime     time.Time
	HalfOpenAttempts int
}

// cbAction is the decision made in phase 1 of the two-phase state transition.
type cbAction int

const (
	cbPass   cbAction = iota // call fn and record result
	cbProbe                  // half-open probe: call fn and record result
	cbReject                 // circuit open: reject without calling fn
)

// cbCounter generates unique key names for circuit breaker state.
var cbCounter atomic.Int64

// CircuitBreaker wraps fn with a circuit-breaker state machine. When too many
// consecutive failures occur, the circuit opens and items are rejected with
// [ErrCircuitOpen] for a cooldown period. After cooldown, a probe item tests
// recovery; success closes the circuit, failure re-opens it.
//
// Combine with [OnError](Skip()) to silently drop rejected items:
//
//	results := kitsune.CircuitBreaker(p, callAPI,
//	    []kitsune.CircuitBreakerOption{
//	        kitsune.FailureThreshold(3),
//	        kitsune.CooldownDuration(10 * time.Second),
//	    },
//	    kitsune.OnError(kitsune.Skip()),
//	)
//
// CircuitBreaker is safe for concurrent use: all state transitions are
// protected by the [Ref] mutex. It supports the [Concurrency] option.
func CircuitBreaker[I, O any](
	p *Pipeline[I],
	fn func(context.Context, I) (O, error),
	cbOpts []CircuitBreakerOption,
	opts ...StageOption,
) *Pipeline[O] {
	cfg := cbConfig{
		failureThreshold: 5,
		cooldown:         30 * time.Second,
		halfOpenProbes:   1,
	}
	for _, o := range cbOpts {
		o(&cfg)
	}

	keyName := fmt.Sprintf("kitsune:cb:%d", cbCounter.Add(1))
	key := NewKey[cbState](keyName, cbState{State: CircuitClosed})

	return MapWith(p, key, func(ctx context.Context, ref *Ref[cbState], item I) (O, error) {
		var zero O

		// ----------------------------------------------------------------
		// Phase 1: check current state and decide what to do.
		// ----------------------------------------------------------------
		var action cbAction
		decideErr := ref.Update(ctx, func(s cbState) (cbState, error) {
			switch s.State {
			case CircuitOpen:
				if time.Since(s.LastFailTime) >= cfg.cooldown {
					// Cooldown expired → transition to half-open.
					s.State = CircuitHalfOpen
					s.HalfOpenAttempts = 0
					action = cbProbe
				} else {
					action = cbReject
				}
			case CircuitHalfOpen:
				if s.HalfOpenAttempts < cfg.halfOpenProbes {
					s.HalfOpenAttempts++
					action = cbProbe
				} else {
					// Already at probe limit → treat like open until probe resolves.
					action = cbReject
				}
			default: // CircuitClosed
				action = cbPass
			}
			return s, nil
		})
		if decideErr != nil {
			return zero, decideErr
		}
		if action == cbReject {
			return zero, ErrCircuitOpen
		}

		// ----------------------------------------------------------------
		// Phase 2: call fn outside the lock.
		// ----------------------------------------------------------------
		result, fnErr := fn(ctx, item)

		// ----------------------------------------------------------------
		// Phase 3: record outcome and transition state.
		// ----------------------------------------------------------------
		updateErr := ref.Update(ctx, func(s cbState) (cbState, error) {
			if fnErr != nil {
				s.ConsecutiveFails++
				s.LastFailTime = time.Now()
				if action == cbProbe || s.ConsecutiveFails >= cfg.failureThreshold {
					s.State = CircuitOpen
					s.HalfOpenAttempts = 0
				}
			} else {
				// Success
				s.ConsecutiveFails = 0
				if action == cbProbe {
					s.State = CircuitClosed
					s.HalfOpenAttempts = 0
				}
			}
			return s, nil
		})
		if updateErr != nil {
			return zero, updateErr
		}
		return result, fnErr
	}, opts...)
}
