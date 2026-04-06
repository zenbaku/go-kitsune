package kitsune

import (
	"context"
	"errors"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Core type aliases — re-export internal types so users don't import internal
// ---------------------------------------------------------------------------

// Clock abstracts time operations for deterministic testing.
// Use testkit.NewTestClock() for deterministic, sleep-free tests.
// Re-exported from internal so users don't need to import internal packages.
type Clock = internal.Clock

// Hook receives lifecycle and per-item events during pipeline execution.
// Defined in internal; re-exported here so users don't import internal packages.
type Hook = internal.Hook

// OverflowHook is an optional extension of [Hook] for drop events.
// Implement this alongside [Hook] and the runtime will call [OnDrop] for every
// item dropped due to a full buffer. Checked via type assertion — existing
// Hook implementations need not implement this.
type OverflowHook = internal.OverflowHook

// SupervisionHook is an optional extension of [Hook] for stage restart events.
// Checked via type assertion — existing Hook implementations need not implement this.
type SupervisionHook = internal.SupervisionHook

// SampleHook is an optional extension of [Hook] for item value sampling.
// If the hook passed to [WithHook] implements SampleHook, the runtime calls
// [OnItemSample] for approximately every 10th successful item exiting Map,
// FlatMap, and Sink stages. Use this to build live data-trail visualizations.
type SampleHook = internal.SampleHook

// GraphHook is an optional extension of [Hook] for graph topology.
// If the hook passed to [WithHook] implements GraphHook, [Runner.Run] calls
// [OnGraph] once before execution begins with the full compiled node list.
type GraphHook = internal.GraphHook

// GraphNode describes a single stage in the pipeline DAG, passed to [GraphHook.OnGraph].
type GraphNode = internal.GraphNode

// BufferHook is an optional extension of [Hook] for observing channel backpressure.
// The engine calls [OnBuffers] once before execution with a query function that
// returns a snapshot of all inter-stage channel occupancies when invoked.
// Call the query periodically to track fill levels over time.
// Checked via type assertion — existing Hook implementations need not implement this.
type BufferHook = internal.BufferHook

// BufferStatus reports the current fill level of one stage's output channel.
type BufferStatus = internal.BufferStatus

// Codec serialises and deserialises values for [Store]-backed state and [CacheBy]
// stages. Implement this interface to substitute a binary format such as
// encoding/gob, protobuf, or msgpack. Register with [WithCodec].
// The default implementation uses encoding/json.
type Codec = internal.Codec

// StageError is returned by [Runner.Run] when a user-supplied stage function
// fails. It carries the originating stage name, the zero-based attempt index
// (>0 after retries), and the underlying cause.
//
//	var se *kitsune.StageError
//	if errors.As(err, &se) {
//	    log.Printf("stage %q failed on attempt %d: %v", se.Stage, se.Attempt, se.Cause)
//	}
type StageError = internal.StageError

// Gate controls pause/resume of source stages in a running pipeline.
// Create with [NewGate] and pass to [WithPauseGate] for use with [Runner.Run],
// or obtain one automatically from [RunHandle] when using [Runner.RunAsync].
type Gate = internal.Gate

// NewGate returns a new [Gate] in the open (unpaused) state.
func NewGate() *Gate { return internal.NewGate() }

// Store is the backend interface for pipeline state persistence.
// [MemoryStore] is the default. External stores (Redis, DynamoDB) can
// implement this interface with []byte serialization.
//
// Users own connection lifecycle — create, configure, and close the
// backing client. Kitsune will never open or close connections.
type Store = internal.Store

// Cache supports key-value caching with TTL. Use it with the [CacheBy] stage
// option on [Map] to skip redundant calls on repeated keys.
// External implementations (Redis, Memcached) can implement this interface.
type Cache = internal.Cache

// DedupSet tracks seen keys for use with [Dedupe].
// External implementations (Redis SETNX, Bloom filters) can implement this interface.
type DedupSet = internal.DedupSet

// JSONCodec is the default [Codec] implementation backed by [encoding/json].
type JSONCodec = internal.JSONCodec

// MemoryStore returns an in-process, mutex-protected state store.
// Useful for testing or when pipeline state does not need to survive restarts.
func MemoryStore() Store { return internal.MemoryStore() }

// MemoryCache returns an in-process cache with a maximum number of entries.
// When full, the oldest entry is evicted. TTL is respected on reads.
func MemoryCache(maxSize int) Cache { return internal.MemoryCache(maxSize) }

// MemoryDedupSet returns an in-process deduplication set.
func MemoryDedupSet() DedupSet { return internal.MemoryDedupSet() }

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Runner holds a compiled pipeline for deferred execution.
// No goroutines start until [Runner.Run] is called.
// The same Runner (and the Pipeline[T] values it was built from) may be Run
// multiple times; each call allocates a fresh, independent channel graph.
type Runner struct {
	// terminal builds the full stage graph into rc when called.
	// It is set by ForEachRunner.Build() / ForEachRunner.Run().
	terminal func(rc *runCtx)
}

// ErrNoRunners is returned by [MergeRunners] when called with no arguments.
var ErrNoRunners = errors.New("kitsune: MergeRunners requires at least one runner")

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

// Run executes the pipeline. It blocks until the pipeline completes,
// the context is cancelled, or an unhandled error occurs.
// Run may be called multiple times; each call builds a fresh channel graph.
func (r *Runner) Run(ctx context.Context, opts ...RunOption) error {
	cfg := buildRunConfig(opts)

	codec := cfg.codec
	if codec == nil {
		codec = internal.JSONCodec{}
	}

	hook := cfg.hook
	if hook == nil {
		hook = internal.NoopHook{}
	}

	// Materialise the pipeline: build functions are called recursively,
	// allocating fresh channels and registering stage functions into rc.
	// Stateful operators (MapWith etc.) register key factories into rc.refs
	// during this phase.
	rc := newRunCtx()
	rc.cache = cfg.defaultCache
	rc.cacheTTL = cfg.defaultCacheTTL
	rc.codec = codec
	rc.hook = hook
	r.terminal(rc)

	// Initialise all Refs with the configured store and codec now that all
	// key factories have been registered by the build phase above.
	rc.refs.init(cfg.store, codec)

	// Notify GraphHook (static topology — channel sizes are at initial 0).
	if gh, ok := hook.(internal.GraphHook); ok {
		nodes := make([]internal.GraphNode, 0, len(rc.metas))
		for _, m := range rc.metas {
			nodes = append(nodes, internal.GraphNode{
				ID:             m.id,
				Name:           m.name,
				Kind:           m.kind,
				Inputs:         m.inputs,
				Concurrency:    m.concurrency,
				Buffer:         m.buffer,
				Overflow:       int(m.overflow),
				BatchSize:      m.batchSize,
				Timeout:        m.timeout,
				HasRetry:       m.hasRetry,
				HasSupervision: m.hasSuperv,
			})
		}
		gh.OnGraph(nodes)
	}

	// Notify BufferHook with a live query closure over the materialised channels.
	if bh, ok := hook.(internal.BufferHook); ok {
		metas := rc.metas
		bh.OnBuffers(func() []internal.BufferStatus {
			out := make([]internal.BufferStatus, 0, len(metas))
			for _, m := range metas {
				if m.getChanLen == nil {
					continue
				}
				out = append(out, internal.BufferStatus{
					Stage:    m.name,
					Length:   m.getChanLen(),
					Capacity: m.getChanCap(),
				})
			}
			return out
		})
	}

	wrappers := make([]func(context.Context) error, len(rc.stages))
	for i, s := range rc.stages {
		s := s
		wrappers[i] = func(ctx context.Context) error { return s(ctx) }
	}

	if cfg.drainTimeout > 0 {
		return runWithDrain(ctx, cfg.drainTimeout, wrappers)
	}
	return internal.RunStages(ctx, wrappers)
}

// runWithDrain executes stages with graceful drain semantics.
// When parentCtx is cancelled, stages are given drainTimeout to finish.
func runWithDrain(parentCtx context.Context, drainTimeout time.Duration, stages []func(context.Context) error) error {
	drainCtx, drainCancel := context.WithCancel(context.Background())
	defer drainCancel()

	go func() {
		select {
		case <-parentCtx.Done():
			select {
			case <-time.After(drainTimeout):
				drainCancel()
			case <-drainCtx.Done():
			}
		case <-drainCtx.Done():
		}
	}()

	return internal.RunStages(drainCtx, stages)
}

// RunAsync starts the pipeline in a background goroutine and returns a
// [RunHandle] for observing completion and controlling execution.
// A [Gate] is created automatically and exposed via [RunHandle.Pause] and
// [RunHandle.Resume]. Pass [WithPauseGate] to supply your own gate instead.
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle {
	cfg := buildRunConfig(opts)
	gate := cfg.gate
	if gate == nil {
		gate = internal.NewGate()
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
// [ErrNoRunners] if called with no arguments.
// MergeRunners combines multiple runners that share the same pipeline graph
// into a single runner. Use this when a pipeline forks (e.g., via [Partition]
// or [Broadcast]) into multiple terminal branches.
//
//	valid, invalid := kitsune.Partition(parsed, isValid)
//	stored := valid.ForEach(storeEvent).Build()
//	logged := invalid.ForEach(logRejection).Build()
//	runner, _ := kitsune.MergeRunners(stored, logged)
//	err := runner.Run(ctx)
//
// Returns [ErrNoRunners] if called with no arguments.
func MergeRunners(runners ...*Runner) (*Runner, error) {
	if len(runners) == 0 {
		return nil, ErrNoRunners
	}
	// Collect all terminal functions into a single combined terminal.
	// When Run is called, a single runCtx is shared, so shared upstream stages
	// are built only once (memoised by stage ID).
	terminals := make([]func(*runCtx), len(runners))
	for i, r := range runners {
		terminals[i] = r.terminal
	}
	return &Runner{
		terminal: func(rc *runCtx) {
			for _, t := range terminals {
				t(rc)
			}
		},
	}, nil
}
