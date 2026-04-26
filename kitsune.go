package kitsune

import (
	"context"
	"errors"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Core type aliases: re-export internal types so users don't import internal
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
// item dropped due to a full buffer. Checked via type assertion; existing
// Hook implementations need not implement this.
type OverflowHook = internal.OverflowHook

// SupervisionHook is an optional extension of [Hook] for stage restart events.
// Checked via type assertion; existing Hook implementations need not implement this.
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
// Checked via type assertion; existing Hook implementations need not implement this.
type BufferHook = internal.BufferHook

// BufferStatus reports the current fill level of one stage's output channel.
type BufferStatus = internal.BufferStatus

// ContextCarrier is implemented by item types that carry a context.Context
// with an attached trace span (or any other per-item context values).
// When an item implements ContextCarrier, the engine uses its context for
// stage function calls, allowing stage functions to create per-item child
// spans with a normal tracer.Start(ctx, ...) call, with no changes to stage
// signatures or pipeline wiring.
//
// Cancellation always comes from the pipeline stage context so that shutdown
// and per-item timeouts work correctly. The item's context contributes only
// its values (e.g. the active trace span).
//
// For third-party types that cannot implement this interface (Kafka messages,
// protobuf-generated types, stdlib types), use [WithContextMapper] instead.
//
// See doc/tracing.md for a comparison table and worked examples for both approaches.
//
// Example:
//
//	type Order struct {
//	    ID  string
//	    ctx context.Context // set at ingestion from HTTP request or queue message
//	}
//
//	func (o Order) Context() context.Context { return o.ctx }
//
//	// In a stage function ; ctx now carries o's trace span automatically:
//	kitsune.Map(orders, func(ctx context.Context, o Order) (Invoice, error) {
//	    ctx, span := tracer.Start(ctx, "build-invoice")
//	    defer span.End()
//	    // ...
//	})
type ContextCarrier = internal.ContextCarrier

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
// Users own connection lifecycle: create, configure, and close the
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

// BloomDedupSet returns an in-process probabilistic deduplication set backed
// by a Bloom filter. Memory usage is bounded regardless of key-space size, at
// the cost of a configurable false-positive rate: items may occasionally be
// reported as seen when they have not been. Inserted items are never missed
// (zero false-negative rate).
//
// expectedItems is the anticipated number of unique keys.
// falsePositiveRate is the desired probability of a false positive (e.g. 0.01
// for 1%). Panics if expectedItems <= 0 or falsePositiveRate is not in (0,1).
func BloomDedupSet(expectedItems int, falsePositiveRate float64) DedupSet {
	return internal.BloomDedupSet(expectedItems, falsePositiveRate)
}

// TTLDedupSet returns an in-process deduplication set that forgets keys after
// ttl has elapsed since they were last added. Memory is bounded by the set of
// currently non-expired keys. Eviction is lazy: expired entries are purged on
// the next Contains or Add call; there is no background goroutine.
//
// Re-adding an existing key refreshes its expiry (touch semantics).
// Panics if ttl <= 0.
func TTLDedupSet(ttl time.Duration) DedupSet { return internal.TTLDedupSet(ttl) }

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Runnable is implemented by any value that can produce a [*Runner] for
// execution. It is the argument type accepted by [MergeRunners].
//
// Both [*Runner] and [*ForEachRunner] satisfy this interface, so terminal
// stages can be passed directly to MergeRunners without first calling Build().
type Runnable interface {
	Build() *Runner
}

// Runner holds a compiled pipeline for deferred execution.
// No goroutines start until [Runner.Run] is called.
// The same Runner (and the Pipeline[T] values it was built from) may be Run
// multiple times; each call allocates a fresh, independent channel graph.
type Runner struct {
	// terminal builds the full stage graph into rc when called.
	terminal func(rc *runCtx)

	// finalizers run in registration order after the pipeline completes.
	// Each receives the RunSummary computed before any finalizer ran;
	// their errors are recorded in RunSummary.FinalizerErrs but do not
	// change RunSummary.Outcome.
	finalizers []func(ctx context.Context, s RunSummary) error
}

// Build returns r itself. It exists so that [*Runner] satisfies the
// [Runnable] interface.
func (r *Runner) Build() *Runner { return r }

// WithFinalizer registers fn to run after the pipeline completes. Multiple
// finalizers run in registration order. Each finalizer receives the
// [RunSummary] for the run; its return error (if any) is captured in
// RunSummary.FinalizerErrs. Finalizer errors do not change RunSummary.Outcome
// (finalizers observe the run's outcome, they do not influence it).
//
// The same context that was passed to Run is passed to the finalizer.
//
// WithFinalizer returns r so callers can chain attach calls.
func (r *Runner) WithFinalizer(fn func(ctx context.Context, s RunSummary) error) *Runner {
	r.finalizers = append(r.finalizers, fn)
	return r
}

// ErrNoRunners is returned by [MergeRunners] when called with no arguments.
var ErrNoRunners = errors.New("kitsune: MergeRunners requires at least one runner")

// RunHandle is returned by [Runner.RunAsync]. It provides multiple ways to
// observe the pipeline's completion and to pause/resume source stages.
type RunHandle struct {
	done    chan struct{}
	err     error
	summary RunSummary // written before done is closed; safe to read after <-done returns
	gate    *Gate
}

// Wait blocks until the pipeline completes and returns its [RunSummary] and
// fatal error (if any). Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Wait() (RunSummary, error) {
	<-h.done
	return h.summary, h.err
}

// Done returns a channel that is closed when the pipeline completes.
// Use this in a select alongside other channels.
func (h *RunHandle) Done() <-chan struct{} {
	return h.done
}

// Err returns the pipeline's terminal error. It is non-blocking: if the
// pipeline has not yet completed it returns nil. Use [RunHandle.Done] in a
// select and then call Err to retrieve the result, or use [RunHandle.Wait]
// to block until done. Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Err() error {
	select {
	case <-h.done:
		return h.err
	default:
		return nil
	}
}

// Summary returns the run's summary. It is non-blocking: if the pipeline has
// not yet completed it returns the zero-valued [RunSummary] (Outcome:
// RunSuccess, all other fields zero). Use [RunHandle.Done] in a select and
// then call Summary to retrieve the result, or use [RunHandle.Wait] to block
// until done. Safe to call from multiple goroutines concurrently.
func (h *RunHandle) Summary() RunSummary {
	select {
	case <-h.done:
		return h.summary
	default:
		return RunSummary{}
	}
}

// Pause stops sources from emitting new items. In-flight items continue
// draining through downstream stages. Safe to call multiple times. Has no
// effect after the pipeline has completed.
func (h *RunHandle) Pause() { h.gate.Pause() }

// Resume allows sources to emit items again after a [RunHandle.Pause]. Safe
// to call multiple times. Has no effect if the pipeline is not paused.
func (h *RunHandle) Resume() { h.gate.Resume() }

// Paused reports whether the pipeline is currently paused.
func (h *RunHandle) Paused() bool { return h.gate.Paused() }

// Run executes the pipeline. It blocks until the pipeline completes,
// the context is cancelled, or an unhandled error occurs. Run may be called
// multiple times; each call builds a fresh channel graph.
//
// Returns a [RunSummary] describing the run plus the same fatal error (if
// any) that previously was the only return value. The summary is populated
// even when the error is non-nil. Finalizers attached via [Runner.WithFinalizer]
// run after the stage graph completes (and after the summary is computed)
// in registration order; their errors are recorded in RunSummary.FinalizerErrs
// without affecting RunSummary.Outcome.
func (r *Runner) Run(ctx context.Context, opts ...RunOption) (RunSummary, error) {
	cfg := buildRunConfig(opts)

	codec := cfg.codec
	if codec == nil {
		codec = internal.JSONCodec{}
	}

	hook := cfg.hook
	if hook == nil {
		hook = internal.NoopHook{}
	}

	rc := newRunCtx()
	rc.cache = cfg.defaultCache
	rc.cacheTTL = cfg.defaultCacheTTL
	rc.codec = codec
	rc.hook = hook
	rc.gate = cfg.gate
	rc.defaultErrorHandler = cfg.defaultErrorHandler
	rc.defaultBuffer = cfg.defaultBuffer
	rc.defaultKeyTTL = cfg.defaultKeyTTL
	rc.dryRun = cfg.dryRun
	rc.devStore = cfg.devStore
	r.terminal(rc)

	rc.refs.init(cfg.store, codec)

	if gh, ok := hook.(internal.GraphHook); ok {
		gh.OnGraph(metasToGraphNodes(rc.metas))
	}

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

	started := time.Now()
	var pipelineErr error
	if cfg.drainTimeout > 0 {
		pipelineErr = runWithDrain(ctx, cfg.drainTimeout, rc.signalDone, wrappers)
	} else {
		pipelineErr = internal.RunStages(ctx, wrappers)
	}
	finishedAt := time.Now()

	summary := RunSummary{
		Outcome:     deriveRunOutcome(rc, pipelineErr),
		Err:         pipelineErr,
		Duration:    finishedAt.Sub(started),
		CompletedAt: finishedAt,
	}
	if mh, ok := hook.(*MetricsHook); ok {
		summary.Metrics = mh.Snapshot()
	} else {
		summary.Metrics = MetricsSnapshot{Timestamp: finishedAt, Elapsed: summary.Duration}
	}

	summary.EffectStats = make(map[string]EffectStats, len(rc.effectStats))
	for _, s := range rc.effectStats {
		summary.EffectStats[s.name] = EffectStats{
			Required: s.required,
			Success:  s.success.Load(),
			Failure:  s.failure.Load(),
		}
	}

	if len(r.finalizers) > 0 {
		summary.FinalizerErrs = make([]error, len(r.finalizers))
		for i, fn := range r.finalizers {
			summary.FinalizerErrs[i] = fn(ctx, summary)
		}
	}

	if rsh, ok := hook.(RunSummaryHook); ok {
		rsh.OnRunComplete(ctx, summary)
	}

	return summary, pipelineErr
}

// runWithDrain executes stages with graceful drain semantics using a two-phase
// shutdown:
//
//   - Phase 1: When parentCtx is cancelled, signalDone() closes the rc.done
//     channel. Sources watch rc.done (via the goroutine inside Generate) and
//     cancel their stageCtx, unblocking any parked source without touching the
//     processing context for downstream stages.
//
//   - Phase 2: Downstream stages continue with a fresh drainCtx and have up to
//     drainTimeout to flush any in-flight items naturally (e.g. Batch flushes
//     a partial buffer when its input channel is closed by the stopped source).
//
//   - Hard stop: If drainTimeout elapses before all stages finish, drainCtx is
//     cancelled and any remaining stages receive context.Canceled. That error is
//     suppressed on return because it is an expected drain-termination signal.
func runWithDrain(parentCtx context.Context, drainTimeout time.Duration, signalDone func(), stages []func(context.Context) error) error {
	drainCtx, drainCancel := context.WithCancel(context.Background())
	defer drainCancel()

	go func() {
		select {
		case <-parentCtx.Done():
			// Phase 1: stop sources cleanly (closes rc.done; Generate watches it).
			signalDone()
			// Phase 2: wait for natural drain or hard-stop timeout.
			timer := time.NewTimer(drainTimeout)
			select {
			case <-timer.C:
				drainCancel()
			case <-drainCtx.Done():
				timer.Stop()
			}
		case <-drainCtx.Done():
		}
	}()

	err := internal.RunStages(drainCtx, stages)
	// Suppress context errors caused by the drain timeout hard-stop; they are
	// expected when drainCtx was cancelled after the timeout, not genuine errors.
	if err != nil && parentCtx.Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return nil
	}
	return err
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
	h := &RunHandle{done: make(chan struct{}), gate: gate}
	go func() {
		h.summary, h.err = r.Run(ctx, opts...)
		close(h.done)
	}()
	return h
}

// metasToGraphNodes converts the internal stageMeta slice collected during the
// build phase into the public []internal.GraphNode type used by GraphHook and
// Pipeline.Describe.
func metasToGraphNodes(metas []stageMeta) []internal.GraphNode {
	nodes := make([]internal.GraphNode, 0, len(metas))
	for _, m := range metas {
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
			SegmentName:    m.segmentName,
			IsEffect:       m.isEffect,
			EffectRequired: m.effectRequired,
		})
	}
	return nodes
}

// MergeRunners combines multiple terminal stages that share the same pipeline
// graph into a single runner. Use this when a pipeline forks (e.g., via
// [Partition] or [Broadcast]) into multiple terminal branches.
//
//	valid, invalid := kitsune.Partition(parsed, isValid)
//	runner, _ := kitsune.MergeRunners(
//		valid.ForEach(storeEvent),
//		invalid.ForEach(logRejection),
//	)
//	err := runner.Run(ctx)
//
// Both [*Runner] and [*ForEachRunner] satisfy the [Runnable] interface.
// Returns [ErrNoRunners] if called with no arguments.
func MergeRunners(runners ...Runnable) (*Runner, error) {
	if len(runners) == 0 {
		return nil, ErrNoRunners
	}
	// Collect all terminal functions into a single combined terminal.
	// When Run is called, a single runCtx is shared, so shared upstream stages
	// are built only once (memoised by stage ID).
	terminals := make([]func(*runCtx), len(runners))
	var finalizers []func(ctx context.Context, s RunSummary) error
	for i, r := range runners {
		built := r.Build()
		terminals[i] = built.terminal
		finalizers = append(finalizers, built.finalizers...)
	}
	return &Runner{
		terminal: func(rc *runCtx) {
			for _, t := range terminals {
				t(rc)
			}
		},
		finalizers: finalizers,
	}, nil
}
