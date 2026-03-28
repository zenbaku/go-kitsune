// Package kitsune is a type-safe, concurrent data pipeline library for Go.
//
// Users compose ordinary functions into pipelines using a small set of
// helpers. The runtime handles channels, goroutines, backpressure, error
// routing, and state management transparently.
//
// A minimal pipeline:
//
//	lines  := kitsune.FromSlice(rawLines)
//	parsed := kitsune.Map(lines, parse)
//	err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
package kitsune

import (
	"context"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// Pipeline represents a typed stage output in a data pipeline.
// It is an immutable handle — every operation returns a new Pipeline.
// No processing occurs until [Runner.Run] is called.
type Pipeline[T any] struct {
	g    *engine.Graph
	node int
	port int // 0 normally; 0 (match) or 1 (rest) for partition outputs
}

// Runner holds a compiled pipeline graph for deferred execution.
// No goroutines start until [Runner.Run] is called.
type Runner struct {
	g *engine.Graph
}

// Hook receives lifecycle and per-item events during pipeline execution.
// Defined in the engine; re-exported here so users don't import internal packages.
type Hook = engine.Hook

// OverflowHook is an optional extension of [Hook] for drop events.
// Implement this alongside [Hook] and the runtime will call [OnDrop] for every
// item dropped due to a full buffer. Checked via type assertion — existing
// Hook implementations need not implement this.
type OverflowHook = engine.OverflowHook

// SupervisionHook is an optional extension of [Hook] for stage restart events.
// Checked via type assertion — existing Hook implementations need not implement this.
type SupervisionHook = engine.SupervisionHook

// SampleHook is an optional extension of [Hook] for item value sampling.
// If the hook passed to [WithHook] implements SampleHook, the runtime calls
// [OnItemSample] for approximately every 10th successful item exiting Map,
// FlatMap, and Sink stages. Use this to build live data-trail visualizations.
type SampleHook = engine.SampleHook

// GraphHook is an optional extension of [Hook] for graph topology.
// If the hook passed to [WithHook] implements GraphHook, [Runner.Run] calls
// [OnGraph] once before execution begins with the full compiled node list.
type GraphHook = engine.GraphHook

// GraphNode describes a single stage in the pipeline DAG, passed to [GraphHook.OnGraph].
type GraphNode = engine.GraphNode

// BufferHook is an optional extension of [Hook] for observing channel backpressure.
// The engine calls [OnBuffers] once before execution with a query function that
// returns a snapshot of all inter-stage channel occupancies when invoked.
// Call the query periodically to track fill levels over time.
// Checked via type assertion — existing Hook implementations need not implement this.
type BufferHook = engine.BufferHook

// BufferStatus reports the current fill level of one stage's output channel.
type BufferStatus = engine.BufferStatus

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Run executes the pipeline. It blocks until the pipeline completes,
// the context is cancelled, or an unhandled error occurs.
func (r *Runner) Run(ctx context.Context, opts ...RunOption) error {
	cfg := buildRunConfig(opts)
	return engine.Run(ctx, r.g, engine.RunConfig{
		Hook:            cfg.hook,
		Store:           cfg.store,
		DrainTimeout:    cfg.drainTimeout,
		DefaultCache:    cfg.defaultCache,
		DefaultCacheTTL: cfg.defaultCacheTTL,
	})
}

// RunAsync starts the pipeline in a background goroutine and returns a channel
// that receives exactly one value: nil on clean completion, or the first error.
// It is the non-blocking counterpart to [Runner.Run].
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- r.Run(ctx, opts...) }()
	return errCh
}

// MergeRunners combines multiple runners that share the same pipeline graph
// into a single runner. Use this when a pipeline forks (e.g., via [Partition]
// or [Broadcast]) into multiple terminal branches.
//
//	valid, invalid := kitsune.Partition(parsed, isValid)
//	stored := valid.ForEach(storeEvent)
//	logged := invalid.ForEach(logRejection)
//	err := kitsune.MergeRunners(stored, logged).Run(ctx)
//
// MergeRunners panics if no runners are provided or if the runners do not
// share the same pipeline graph.
func MergeRunners(runners ...*Runner) *Runner {
	if len(runners) == 0 {
		panic("kitsune: MergeRunners requires at least one runner")
	}
	g := runners[0].g
	for _, r := range runners[1:] {
		if r.g != g {
			panic("kitsune: MergeRunners requires all runners to share the same pipeline graph")
		}
	}
	return runners[0]
}
