package kitsune

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// RateLimit
// ---------------------------------------------------------------------------

// RateLimitMode controls what RateLimit does when the token bucket is empty.
type RateLimitMode int

const (
	// RateLimitWait blocks the pipeline until a token is available (backpressure).
	// This is the default mode.
	RateLimitWait RateLimitMode = iota
	// RateLimitDrop silently discards items when the token bucket is empty.
	RateLimitDrop
)

// RateLimitOpt configures a [RateLimit] stage.
type RateLimitOpt func(*rateLimitConfig)

type rateLimitConfig struct {
	burst int
	mode  RateLimitMode
}

// Burst sets the burst size (maximum token accumulation) for [RateLimit].
// Defaults to 1 when not set.
func Burst(n int) RateLimitOpt {
	return func(c *rateLimitConfig) {
		if n > 0 {
			c.burst = n
		}
	}
}

// RateMode sets the overflow mode for [RateLimit] (default: [RateLimitWait]).
func RateMode(m RateLimitMode) RateLimitOpt {
	return func(c *rateLimitConfig) { c.mode = m }
}

// RateLimit limits how quickly items flow through the pipeline.
//
// ratePerSec is the steady-state throughput in items per second.
// With [RateLimitWait] (default), the pipeline blocks when the token bucket is
// empty, creating backpressure. With [RateLimitDrop], excess items are silently
// discarded. Use [Burst] to allow short bursts above the steady-state rate.
//
// rlOpts configures rate-limit behaviour ([Burst], [RateMode]). stageOpts
// accepts any [StageOption] — most usefully [WithName] and [Buffer].
//
//	// Allow up to 100 events/sec with bursts of up to 10:
//	kitsune.RateLimit(events, 100, []kitsune.RateLimitOpt{kitsune.Burst(10)})
//
//	// Drop items that exceed 50/sec instead of blocking:
//	kitsune.RateLimit(events, 50, []kitsune.RateLimitOpt{kitsune.RateMode(kitsune.RateLimitDrop)})
func RateLimit[T any](p *Pipeline[T], ratePerSec float64, rlOpts []RateLimitOpt, stageOpts ...StageOption) *Pipeline[T] {
	track(p)
	cfg := rateLimitConfig{burst: 1}
	for _, o := range rlOpts {
		o(&cfg)
	}
	scfg := buildStageConfig(stageOpts)
	id := nextPipelineID()
	name := "rate_limit"
	if scfg.name != "" {
		name = scfg.name
	}
	meta := stageMeta{
		id:     id,
		kind:   "rate_limit",
		name:   name,
		buffer: scfg.buffer,
		inputs: []int64{p.id},
	}
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		inCh := p.build(rc)
		buf := rc.effectiveBufSize(scfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		limiter := rate.NewLimiter(rate.Limit(ratePerSec), cfg.burst)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if cfg.mode == RateLimitDrop {
						if !limiter.Allow() {
							continue
						}
					} else {
						if err := limiter.Wait(ctx); err != nil {
							return err
						}
					}
					if err := outbox.Send(ctx, item); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// CircuitBreaker
// ---------------------------------------------------------------------------

// ErrCircuitOpen is emitted by [CircuitBreaker] stages when the circuit is open.
var ErrCircuitOpen = errors.New("kitsune: circuit breaker open")

// CircuitBreakerOpt configures a [CircuitBreaker] stage.
type CircuitBreakerOpt func(*circuitBreakerConfig)

type circuitBreakerConfig struct {
	failureThreshold int
	cooldown         time.Duration
	halfOpenProbes   int
	halfOpenTimeout  time.Duration
}

// FailureThreshold sets the number of consecutive failures needed to open the
// circuit (default: 5).
func FailureThreshold(n int) CircuitBreakerOpt {
	return func(c *circuitBreakerConfig) {
		if n > 0 {
			c.failureThreshold = n
		}
	}
}

// CooldownDuration sets how long the circuit stays open before entering the
// half-open state (default: 10s).
func CooldownDuration(d time.Duration) CircuitBreakerOpt {
	return func(c *circuitBreakerConfig) {
		if d > 0 {
			c.cooldown = d
		}
	}
}

// HalfOpenProbes sets how many successful probes are required to close the
// circuit from the half-open state (default: 1).
func HalfOpenProbes(n int) CircuitBreakerOpt {
	return func(c *circuitBreakerConfig) {
		if n > 0 {
			c.halfOpenProbes = n
		}
	}
}

// HalfOpenTimeout sets a deadline on the half-open state. If the required
// number of successful probes has not been received within d, the circuit
// opens again (resetting the cooldown clock). No timeout is applied by
// default.
func HalfOpenTimeout(d time.Duration) CircuitBreakerOpt {
	return func(c *circuitBreakerConfig) {
		if d > 0 {
			c.halfOpenTimeout = d
		}
	}
}

type cbState int

const (
	cbClosed   cbState = iota // normal operation
	cbOpen                    // tripped: reject all items
	cbHalfOpen                // probing: allow limited items through
)

type circuitBreaker struct {
	mu               sync.Mutex
	state            cbState
	failures         int
	successesNeeded  int
	openUntil        time.Time
	halfOpenDeadline time.Time
	cfg              circuitBreakerConfig
}

func (cb *circuitBreaker) allow(now time.Time) (allowed bool, open bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbClosed:
		return true, false

	case cbOpen:
		if now.Before(cb.openUntil) {
			return false, true
		}
		// Cooldown elapsed — enter half-open.
		cb.state = cbHalfOpen
		cb.successesNeeded = cb.cfg.halfOpenProbes
		if cb.cfg.halfOpenTimeout > 0 {
			cb.halfOpenDeadline = now.Add(cb.cfg.halfOpenTimeout)
		}
		return true, false

	case cbHalfOpen:
		if cb.cfg.halfOpenTimeout > 0 && now.After(cb.halfOpenDeadline) {
			// Half-open window expired without enough successes — reopen.
			cb.state = cbOpen
			cb.openUntil = now.Add(cb.cfg.cooldown)
			cb.failures = 0
			cb.halfOpenDeadline = time.Time{}
			return false, true
		}
		return true, false
	}
	return false, false
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == cbHalfOpen {
		cb.successesNeeded--
		if cb.successesNeeded <= 0 {
			cb.state = cbClosed
			cb.failures = 0
			cb.halfOpenDeadline = time.Time{}
		}
	} else {
		cb.failures = 0
	}
}

func (cb *circuitBreaker) recordFailure(now time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	if cb.state == cbHalfOpen || cb.failures >= cb.cfg.failureThreshold {
		cb.state = cbOpen
		cb.openUntil = now.Add(cb.cfg.cooldown)
		cb.failures = 0
	}
}

// CircuitBreaker wraps fn in a circuit breaker that opens after consecutive
// failures and re-tests after a cooldown period.
//
// States:
//   - Closed (normal): fn is called for every item; consecutive failures
//     increment a counter.
//   - Open (tripped): once FailureThreshold consecutive failures occur, the
//     circuit opens. All items immediately receive [ErrCircuitOpen] without
//     calling fn. The circuit stays open for CooldownDuration.
//   - Half-Open (probing): after the cooldown, a limited number of items
//     (HalfOpenProbes) are let through. If all succeed, the circuit closes.
//     If any fail, the circuit opens again.
//
// All [StageOption]s apply to the underlying [Map] stage, so [OnError] controls
// what happens when [ErrCircuitOpen] or fn errors are returned. Use [Skip] to
// silently drop items while the circuit is open, or [Return] to substitute a
// default value.
//
//	breaker := kitsune.CircuitBreaker(p, callExternalAPI,
//	    kitsune.FailureThreshold(3), kitsune.CooldownDuration(30*time.Second),
//	    kitsune.HalfOpenProbes(2),
//	    kitsune.OnError(kitsune.ActionDrop()), // drop items while open
//	)
func CircuitBreaker[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), cbOpts []CircuitBreakerOpt, stageOpts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := circuitBreakerConfig{
		failureThreshold: 5,
		cooldown:         10 * time.Second,
		halfOpenProbes:   1,
	}
	for _, o := range cbOpts {
		o(&cfg)
	}

	cb := &circuitBreaker{cfg: cfg}
	wrapped := func(ctx context.Context, item I) (O, error) {
		allowed, open := cb.allow(time.Now())
		if open || !allowed {
			var zero O
			return zero, ErrCircuitOpen
		}
		out, err := fn(ctx, item)
		if err != nil {
			cb.recordFailure(time.Now())
			return out, err
		}
		cb.recordSuccess()
		return out, nil
	}

	// Build on top of Map so all StageOptions (OnError, Concurrency, Buffer, …)
	// work transparently.
	stageOpts = append([]StageOption{WithName("circuit_breaker")}, stageOpts...)
	return Map(p, wrapped, stageOpts...)
}
