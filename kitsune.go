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

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Run executes the pipeline. It blocks until the pipeline completes,
// the context is cancelled, or an unhandled error occurs.
func (r *Runner) Run(ctx context.Context, opts ...RunOption) error {
	cfg := buildRunConfig(opts)
	return engine.Run(ctx, r.g, engine.RunConfig{Hook: cfg.hook, Store: cfg.store})
}

// MergeRunners combines multiple runners that share the same pipeline graph
// into a single runner. Use this when a pipeline forks (e.g., via [Partition])
// into multiple terminal branches.
//
//	valid, invalid := kitsune.Partition(parsed, isValid)
//	stored := valid.ForEach(storeEvent)
//	logged := invalid.ForEach(logRejection)
//	err := kitsune.MergeRunners(stored, logged).Run(ctx)
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
