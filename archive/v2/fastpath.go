package kitsune

import "github.com/zenbaku/go-kitsune/v2/internal"

// isFastPathEligible returns true when a serial stage may use the optimised
// drain-protocol + micro-batching loop instead of the full slow path.
//
// Conditions (must ALL hold):
//   - Concurrency(1)          — single goroutine, no semaphore needed
//   - No supervision          — pre-fetched items must not be silently lost on restart
//   - DefaultHandler          — no retry / skip / return-value substitution
//   - OverflowBlock           — default backpressure; DropNewest/Oldest need the outbox
//   - No per-item timeout     — fast path skips itemContext
//   - NoopHook                — fast path skips all hook dispatch
//
// The cache condition (cfg.cacheConfig == nil) is checked at the call site for
// Map, since it involves an additional actualFn wrapping step.
func isFastPathEligible(cfg stageConfig, hook internal.Hook) bool {
	return cfg.concurrency <= 1 &&
		!cfg.supervision.HasSupervision() &&
		internal.IsDefaultHandler(cfg.errorHandler) &&
		cfg.overflow == internal.OverflowBlock &&
		cfg.timeout == 0 &&
		internal.IsNoopHook(hook)
}

// isFastPathEligibleCfg returns true when cfg alone (without the hook) satisfies
// the fast-path conditions.  Used at construction time to decide whether to set
// Pipeline.fusionEntry.  The NoopHook check is deferred to run time.
func isFastPathEligibleCfg(cfg stageConfig) bool {
	return cfg.concurrency <= 1 &&
		!cfg.supervision.HasSupervision() &&
		internal.IsDefaultHandler(cfg.errorHandler) &&
		cfg.overflow == internal.OverflowBlock &&
		cfg.timeout == 0
}

// track increments p's consumerCount for fan-out detection in typed fusion.
// It is a no-op when p has no fusionEntry (e.g. sources, FlatMap outputs).
// Call once at construction time in every operator or terminal that consumes p.
func track[T any](p *Pipeline[T]) {
	if p.fusionEntry != nil {
		p.consumerCount.Add(1)
	}
}
