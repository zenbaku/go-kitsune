package kitsune

import (
	"fmt"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// OptimizationReport
// ---------------------------------------------------------------------------

// OptimizationReport describes the execution model of a single pipeline stage
// as seen by [Pipeline.IsOptimized].
//
// The two optimisations reported here are:
//
//   - Fast path: the stage uses the drain-protocol + micro-batching loop
//     (skips per-item hook calls, time.Now, and the full ProcessItem wrapper),
//     giving significantly higher throughput for serial, hook-free chains.
//
//   - Fusion: the stage is composed into a single goroutine with its
//     downstream stage, eliminating the inter-stage channel hop entirely.
//     Only possible when FastPath is true and this stage has exactly one consumer.
//
// [Reasons] is populated with a short description of each condition that
// prevents FastPath or Fused from being true. It is empty when both are true.
type OptimizationReport struct {
	// Name is the stage name (set via [WithName] or defaulted from the operator).
	Name string
	// Kind is the operator type ("map", "filter", "source", "sink", etc.).
	Kind string
	// SupportsFastPath is true for operators that have a fast-path
	// implementation (currently Map and Filter). Operators like Batch, FlatMap,
	// or source generators do not have a fast path.
	SupportsFastPath bool
	// FastPath is true when the stage would use the drain-protocol +
	// micro-batching execution loop. Only meaningful when SupportsFastPath is true.
	FastPath bool
	// Fused is true when the stage would be composed into a single goroutine
	// with its downstream consumer (zero inter-stage channel hop).
	// Only possible when FastPath is true and this stage has exactly one consumer.
	Fused bool
	// Reasons lists conditions that prevent FastPath or Fused from being true.
	// Empty when both FastPath and Fused are true (or when SupportsFastPath is false).
	Reasons []string
}

// ---------------------------------------------------------------------------
// IsOptimized / IsFastPath — methods on Pipeline[T]
// ---------------------------------------------------------------------------

// IsOptimized returns an [OptimizationReport] for every stage in the pipeline
// DAG reachable from p, showing whether each stage would use the fast path
// and/or be fused when Run is called with the given RunOptions.
//
// IsOptimized is non-destructive: it does not start any goroutines. It
// materialises a temporary channel graph (like [Pipeline.Describe]) and
// releases it before returning.
//
// For accurate fusion reports, call IsOptimized after all consumers of p are
// attached (including the [ForEachRunner] terminal). The consumer count is
// recorded at construction time; a stage with zero consumers is reported as
// not fused because no terminal has registered yet.
//
// Example:
//
//	src    := kitsune.FromSlice(items)
//	mapped := kitsune.Map(src, parse)
//	runner := mapped.Filter(isValid).ForEach(store)
//
//	for _, r := range mapped.IsOptimized() {
//	    if r.SupportsFastPath && !r.FastPath {
//	        t.Errorf("stage %s not on fast path: %v", r.Name, r.Reasons)
//	    }
//	}
func (p *Pipeline[T]) IsOptimized(opts ...RunOption) []OptimizationReport {
	rc := newRunCtx()
	rc.hook = internal.NoopHook{}
	rc.codec = internal.JSONCodec{}
	_ = p.build(rc)

	runCfg := buildRunConfig(opts)
	hook := runCfg.hook
	if hook == nil {
		hook = internal.NoopHook{}
	}
	defaultEH := runCfg.defaultErrorHandler

	reports := make([]OptimizationReport, 0, len(rc.metas))
	for _, m := range rc.metas {
		reports = append(reports, optimizationReportForMeta(m, hook, defaultEH))
	}
	return reports
}

// IsFastPath returns true when every fast-path-capable stage in the pipeline
// would use the fast-path execution loop under the given RunOptions.
//
// Stages that do not support the fast path (sources, Batch, FlatMap, etc.) are
// ignored; only Map and Filter stages are checked.
//
// Example:
//
//	if !mapped.IsFastPath() {
//	    t.Error("expected pipeline to be on the fast path")
//	}
func (p *Pipeline[T]) IsFastPath(opts ...RunOption) bool {
	for _, r := range p.IsOptimized(opts...) {
		if r.SupportsFastPath && !r.FastPath {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// internal helper
// ---------------------------------------------------------------------------

// optimizationReportForMeta computes the OptimizationReport for a single
// stageMeta given the effective hook and pipeline-level error handler.
func optimizationReportForMeta(m stageMeta, hook internal.Hook, defaultEH internal.ErrorHandler) OptimizationReport {
	r := OptimizationReport{
		Name:             m.name,
		Kind:             m.kind,
		SupportsFastPath: m.supportsFastPath,
	}

	if !m.supportsFastPath {
		// Source, Batch, FlatMap, etc. do not have a fast-path implementation.
		// Report FastPath: false with no reasons — it is not an error condition.
		return r
	}

	var reasons []string

	// --- Construction-time conditions (from stageConfig) ---
	if m.concurrency > 1 {
		reasons = append(reasons, fmt.Sprintf("Concurrency(%d) (requires 1)", m.concurrency))
	}
	if m.hasSuperv {
		reasons = append(reasons, "Supervise(...) set")
	}
	if m.overflow != internal.OverflowBlock {
		switch m.overflow {
		case internal.OverflowDropNewest:
			reasons = append(reasons, "Overflow(DropNewest) set")
		case internal.OverflowDropOldest:
			reasons = append(reasons, "Overflow(DropOldest) set")
		default:
			reasons = append(reasons, fmt.Sprintf("overflow=%v set", m.overflow))
		}
	}
	if m.timeout > 0 {
		reasons = append(reasons, fmt.Sprintf("Timeout(%v) set", m.timeout))
	}
	// If isFastPathCfg is false but the above individual conditions are all
	// satisfied, the remaining cause is a stage-level error handler or (for
	// Map) a CacheBy option — both checked inside isFastPathEligibleCfg.
	if !m.isFastPathCfg &&
		m.concurrency <= 1 &&
		!m.hasSuperv &&
		m.overflow == internal.OverflowBlock &&
		m.timeout == 0 {
		if m.kind == "map" {
			reasons = append(reasons, "OnError or CacheBy set on this stage")
		} else {
			reasons = append(reasons, "OnError set on this stage")
		}
	}

	// --- Run-time conditions (from RunOptions) ---
	if !internal.IsNoopHook(hook) {
		reasons = append(reasons, fmt.Sprintf("WithHook(%T) set", hook))
	}
	if !internal.IsDefaultHandler(defaultEH) {
		reasons = append(reasons, fmt.Sprintf("WithErrorStrategy(%T) set", defaultEH))
	}

	r.FastPath = len(reasons) == 0

	// --- Fusion: fast path required, plus single-consumer chain ---
	if r.FastPath && m.hasFusionEntry {
		if m.getConsumerCount == nil {
			// No consumer yet — not fused.
			reasons = append(reasons, "no consumer attached yet")
		} else if count := m.getConsumerCount(); count == 1 {
			r.Fused = true
		} else if count == 0 {
			reasons = append(reasons, "no consumer attached yet")
		} else {
			reasons = append(reasons, fmt.Sprintf("fan-out: %d consumers (fusion requires 1)", count))
		}
	}

	r.Reasons = reasons
	return r
}
