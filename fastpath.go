package kitsune

import "github.com/zenbaku/go-kitsune/internal"

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
		cfg.contextMapperFn == nil &&
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
		cfg.timeout == 0 &&
		cfg.contextMapperFn == nil
}

// resolveHandler returns the effective ErrorHandler for a stage, applying the
// pipeline-level default when the stage has no explicit handler (nil).
// Priority: stage-level OnError > pipeline-level WithErrorStrategy > DefaultHandler.
func resolveHandler(cfg stageConfig, rc *runCtx) internal.ErrorHandler {
	if cfg.errorHandler != nil {
		return cfg.errorHandler
	}
	if rc.defaultErrorHandler != nil {
		return rc.defaultErrorHandler
	}
	return internal.DefaultHandler{}
}

// track increments p's consumerCount. Used for two purposes:
//   - fan-out detection in typed fusion (fusion disables itself when count > 1)
//   - cooperative-drain consumer accounting (drainEntry refs = total consumers)
// Call once at construction time in every operator or terminal that consumes p.
func track[T any](p *Pipeline[T]) {
	p.consumerCount.Add(1)
}
