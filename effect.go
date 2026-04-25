package kitsune

import (
	"time"
)

// EffectOutcome carries the result of one [Effect] call.
//
// Input is the original item from the upstream pipeline; Result is the value
// returned by the effect function on success (the zero value of R on failure
// or in dry-run mode); Err is the terminal error after retries are exhausted
// (nil on success); Applied reports whether the effect function returned
// without error during this attempt.
//
// On per-attempt timeout, Applied is false and Err carries the timeout, but
// the underlying side-effect may already have been applied; treat Applied as
// a hint, not a guarantee.
type EffectOutcome[I, R any] struct {
	Input   I
	Result  R
	Err     error
	Applied bool
}

// EffectOption configures an [Effect] or [TryEffect] stage. Both
// [EffectPolicy] (a value) and the call-site option helpers (functions
// returned by [Required], [BestEffort], [AttemptTimeout],
// [WithIdempotencyKey]) satisfy this interface.
//
// Options are applied in argument order; later options overwrite earlier
// ones. To bundle reusable defaults, define an [EffectPolicy] value and pass
// it first, then layer per-call overrides:
//
//	var SNSPolicy = kitsune.EffectPolicy{
//	    Required:       true,
//	    Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(50*time.Millisecond)),
//	    AttemptTimeout: 5 * time.Second,
//	}
//	out := kitsune.Effect(p, publish, SNSPolicy, kitsune.AttemptTimeout(10*time.Second))
type EffectOption interface {
	applyEffect(*effectConfig)
}

// EffectPolicy is a reusable bundle of [Effect] settings. Define one as a
// package-level value and pass it to multiple [Effect] call sites; layer
// per-call overrides as additional [EffectOption] arguments after it.
type EffectPolicy struct {
	// Retry controls how many times the effect function is re-attempted
	// after a failed attempt and the backoff between attempts. The zero
	// value performs a single attempt.
	Retry RetryStrategy

	// Required marks the effect as required for run success. The flag is
	// recorded on stageMeta and propagated to GraphNode; the future
	// RunSummary uses it to derive RunOutcome. The zero value of
	// EffectPolicy is Required: false; the [Effect] operator defaults to
	// Required: true when no Required/BestEffort option is supplied.
	Required bool

	// AttemptTimeout, if positive, is applied to each attempt of the effect
	// function via context.WithTimeout. It is independent of (and combines
	// with, taking the smaller deadline) the stage-level Timeout(d)
	// StageOption.
	AttemptTimeout time.Duration

	// Idempotent declares that the effect function tolerates repeated
	// application of the same input without side effects. v1 records this
	// flag for future use; the operator does not de-duplicate retries
	// against a backing store.
	Idempotent bool

	// IdempotencyKey, if non-nil, is the key function used by external
	// idempotent backends to recognise repeats. v1 records the function
	// pointer for future use.
	IdempotencyKey func(any) string
}

// applyEffect makes EffectPolicy satisfy [EffectOption]: passing a policy
// value to [Effect] applies all of its non-zero fields at once.
func (pol EffectPolicy) applyEffect(c *effectConfig) {
	c.retry = pol.Retry
	c.requiredSet = true
	c.required = pol.Required
	if pol.AttemptTimeout > 0 {
		c.attemptTimeout = pol.AttemptTimeout
	}
	c.idempotent = pol.Idempotent
	if pol.IdempotencyKey != nil {
		c.idempotencyKey = pol.IdempotencyKey
	}
}

// effectConfig is the resolved per-stage state assembled from a series of
// [EffectOption] values applied in order.
type effectConfig struct {
	retry          RetryStrategy
	required       bool
	requiredSet    bool
	attemptTimeout time.Duration
	idempotent     bool
	idempotencyKey func(any) string
	stageOpts      []StageOption
}

// effectOptionFunc adapts a function to the EffectOption interface. Internal
// helper used by Required, BestEffort, AttemptTimeout, WithIdempotencyKey.
type effectOptionFunc func(*effectConfig)

func (f effectOptionFunc) applyEffect(c *effectConfig) { f(c) }

// Required marks the [Effect] as required for run success. Without this (or
// [BestEffort]), an Effect defaults to required.
func Required() EffectOption {
	return effectOptionFunc(func(c *effectConfig) {
		c.required = true
		c.requiredSet = true
	})
}

// BestEffort marks the [Effect] as non-required: a terminal failure is
// recorded in the run summary but does not fail the run.
func BestEffort() EffectOption {
	return effectOptionFunc(func(c *effectConfig) {
		c.required = false
		c.requiredSet = true
	})
}

// AttemptTimeout applies a per-attempt deadline to the effect function via
// context.WithTimeout. d <= 0 disables the timeout. Distinct from the
// stage-level [Timeout] StageOption: when both are set, the earlier deadline
// wins.
func AttemptTimeout(d time.Duration) EffectOption {
	return effectOptionFunc(func(c *effectConfig) { c.attemptTimeout = d })
}

// WithIdempotencyKey supplies a function that produces a stable key for an
// input item. v1 records the key function for future use by external
// idempotent backends; the operator does not de-duplicate against a store.
func WithIdempotencyKey(fn func(any) string) EffectOption {
	return effectOptionFunc(func(c *effectConfig) {
		c.idempotent = true
		c.idempotencyKey = fn
	})
}

// EffectStageOption wraps a [StageOption] (e.g. WithName, Buffer) so it can
// be passed alongside EffectOptions. The wrapped option is applied to the
// underlying stage at construction time.
func EffectStageOption(opt StageOption) EffectOption {
	return effectOptionFunc(func(c *effectConfig) {
		c.stageOpts = append(c.stageOpts, opt)
	})
}

func buildEffectConfig(opts []EffectOption) effectConfig {
	cfg := effectConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt.applyEffect(&cfg)
		}
	}
	if !cfg.requiredSet {
		cfg.required = true
	}
	return cfg
}
