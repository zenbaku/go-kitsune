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
	"errors"

	"github.com/zenbaku/go-kitsune/engine"
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

// Clock abstracts time operations for deterministic testing.
// Use testkit.NewTestClock() for deterministic, sleep-free tests.
// Re-exported from engine so users don't need to import internal packages.
type Clock = engine.Clock

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

// Codec serialises and deserialises values for [Store]-backed state and [CacheBy]
// stages. Implement this interface to substitute a binary format such as
// encoding/gob, protobuf, or msgpack. Register with [WithCodec].
// The default implementation uses encoding/json.
type Codec = engine.Codec

// StageError is returned by [Runner.Run] when a user-supplied stage function
// fails. It carries the originating stage name, the zero-based attempt index
// (>0 after retries), and the underlying cause.
//
//	var se *kitsune.StageError
//	if errors.As(err, &se) {
//	    log.Printf("stage %q failed on attempt %d: %v", se.Stage, se.Attempt, se.Cause)
//	}
type StageError = engine.StageError

// Gate controls pause/resume of source stages in a running pipeline.
// Create with [NewGate] and pass to [WithPauseGate] for use with [Runner.Run],
// or obtain one automatically from [RunHandle] when using [Runner.RunAsync].
type Gate = engine.Gate

// NewGate returns a new [Gate] in the open (unpaused) state.
func NewGate() *Gate { return engine.NewGate() }

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
		SampleRate:      cfg.sampleRate,
		Codec:           cfg.codec,
		Gate:            cfg.gate,
	})
}

// ErrNoRunners is returned by [MergeRunners] when called with no arguments.
var ErrNoRunners = errors.New("kitsune: MergeRunners requires at least one runner")

// ErrGraphMismatch is returned by [MergeRunners] when the provided runners do
// not all share the same pipeline graph.
var ErrGraphMismatch = errors.New("kitsune: MergeRunners requires all runners to share the same pipeline graph")

// RunHandle is returned by [Runner.RunAsync]. It provides multiple ways to
// observe the pipeline's completion and to pause/resume source stages.
type RunHandle struct {
	errCh <-chan error
	done  chan struct{}
	gate  *Gate
}

// Wait blocks until the pipeline completes and returns its error (or nil).
func (h *RunHandle) Wait() error {
	return <-h.errCh
}

// Done returns a channel that is closed when the pipeline completes.
// Use this in a select alongside other channels.
func (h *RunHandle) Done() <-chan struct{} {
	return h.done
}

// Err returns the underlying error channel for use in select statements.
// The channel receives exactly one value: nil on success, or the first error.
func (h *RunHandle) Err() <-chan error {
	return h.errCh
}

// Pause stops sources from emitting new items. In-flight items continue
// draining through downstream stages. Safe to call multiple times.
// Has no effect after the pipeline has completed.
func (h *RunHandle) Pause() { h.gate.Pause() }

// Resume allows sources to emit items again after a [RunHandle.Pause].
// Safe to call multiple times. Has no effect if the pipeline is not paused.
func (h *RunHandle) Resume() { h.gate.Resume() }

// Paused reports whether the pipeline is currently paused.
func (h *RunHandle) Paused() bool { return h.gate.Paused() }

// RunAsync starts the pipeline in a background goroutine and returns a
// [RunHandle] for observing completion and controlling execution.
// A [Gate] is created automatically and exposed via [RunHandle.Pause] and
// [RunHandle.Resume]. Pass [WithPauseGate] to supply your own gate instead.
//
//	h := runner.RunAsync(ctx)
//	if err := h.Wait(); err != nil {
//	    log.Fatal(err)
//	}
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle {
	// Use an externally-supplied gate if one was provided; otherwise create one.
	cfg := buildRunConfig(opts)
	gate := cfg.gate
	if gate == nil {
		gate = engine.NewGate()
		opts = append(opts, WithPauseGate(gate))
	}
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		errCh <- r.Run(ctx, opts...)
		close(done)
	}()
	return &RunHandle{errCh: errCh, done: done, gate: gate}
}

// MergeRunners combines multiple runners that share the same pipeline graph
// into a single runner. Use this when a pipeline forks (e.g., via [Partition]
// or [Broadcast]) into multiple terminal branches.
//
//	valid, invalid := kitsune.Partition(parsed, isValid)
//	stored := valid.ForEach(storeEvent)
//	logged := invalid.ForEach(logRejection)
//	runner, _ := kitsune.MergeRunners(stored, logged)
//	err := runner.Run(ctx)
//
// MergeRunners returns [ErrNoRunners] if called with no arguments, or
// [ErrGraphMismatch] if the runners do not share the same pipeline graph.
func MergeRunners(runners ...*Runner) (*Runner, error) {
	if len(runners) == 0 {
		return nil, ErrNoRunners
	}
	g := runners[0].g
	for _, r := range runners[1:] {
		if r.g != g {
			return nil, ErrGraphMismatch
		}
	}
	return runners[0], nil
}
