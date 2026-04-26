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
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// stageFunc / stageMeta
// ---------------------------------------------------------------------------

// stageFunc is a goroutine body launched by Run.
// The context originates from Runner.Run and is passed at execution time,
// not at pipeline construction time.
type stageFunc func(ctx context.Context) error

// stageMeta holds introspection data for a single stage.
// Static fields (id, name, kind, inputs, …) are set at construction time.
// getChanLen / getChanCap are set during build() when the channel is created.
type stageMeta struct {
	id          int64
	name        string
	kind        string
	inputs      []int64
	concurrency int
	buffer      int
	overflow    internal.Overflow
	batchSize   int
	timeout     time.Duration
	hasRetry    bool
	hasSuperv   bool
	// segmentName is the name of the enclosing Segment as set by Segment.Apply.
	// Empty when the stage is not inside a segment. When segments nest, the
	// innermost segment wins (deepest enclosing Segment owns each stage).
	segmentName string

	// isEffect is true when this stage was constructed by [Effect] or
	// [TryEffect]. Used by the future RunSummary derivation to distinguish
	// pure stages from stages that produce externally-visible side effects.
	isEffect bool

	// effectRequired is true when the stage's [EffectPolicy] / [Required]
	// marks the effect as required for run success. Meaningful only when
	// isEffect is true.
	effectRequired bool

	getChanLen func() int
	getChanCap func() int

	// Optimization metadata: set at construction time, read by IsOptimized.
	//
	// supportsFastPath is true for operators that have a fast-path
	// implementation (Map, Filter). Operators like Batch, FlatMap, or sources
	// do not have a fast path; they always run the full processing loop.
	//
	// isFastPathCfg is the result of isFastPathEligibleCfg at construction
	// time, plus any operator-specific conditions (e.g. cacheConfig == nil for
	// Map). The run-time NoopHook check is deferred to IsOptimized.
	//
	// hasFusionEntry is true when a fusionEntry was set on the output pipeline
	// at construction time. Fusion composes a Map/Filter chain into a single
	// goroutine with a downstream ForEach.
	//
	// getConsumerCount returns the output pipeline's consumerCount at query
	// time. Fusion is used only when this returns 1 (single-consumer chain).
	supportsFastPath bool
	isFastPathCfg    bool
	hasFusionEntry   bool
	getConsumerCount func() int32
}

// ---------------------------------------------------------------------------
// Global stage-ID counter
// ---------------------------------------------------------------------------

var globalIDSeq int64

// nextPipelineID returns a process-unique ID for each constructed stage.
// IDs are used for graph visualisation and runCtx memoisation.
//
// The counter is int64 throughout to avoid truncation on 32-bit platforms:
// casting the result to int would silently wrap at 2^31 on 32-bit targets,
// causing channel-memoisation collisions in runCtx.chans and incorrect DAG
// wiring. Keeping the full int64 eliminates the platform-specific bug.
func nextPipelineID() int64 {
	return atomic.AddInt64(&globalIDSeq, 1)
}

// ---------------------------------------------------------------------------
// runCtx: per-Run execution context
// ---------------------------------------------------------------------------

// drainEntry coordinates the cooperative-drain protocol for one producer stage.
// refs is decremented by each consumer that exits; when it reaches zero the
// ch is closed, unblocking the producer's select loop.
type drainEntry struct {
	ch   chan struct{}
	refs atomic.Int32
}

// effectStat tracks success and terminal-failure counts for a single Effect
// stage during a run.
type effectStat struct {
	name     string       // stage name (cf. stageMeta.name); used for logging
	required bool         // mirrors stageMeta.effectRequired
	success  atomic.Int64
	failure  atomic.Int64
	deduped  atomic.Int64 // items skipped via idempotency-key dedupe
}

// errDrained is returned by a source's send helper when the cooperative drain
// fires. sourceStage converts this to nil so it does not propagate as an error.
var errDrained = errors.New("kitsune: cooperative drain")

// runCtx is created fresh on every Runner.Run call.
// As build functions are called recursively from the terminal back to sources,
// each stage registers its stageFunc here and its output channel is memoised
// by stage ID so that shared upstream stages are only built once per run.
type runCtx struct {
	stages              []stageFunc
	metas               []stageMeta
	chans               map[int64]any // stage ID → chan T (type-erased for storage)
	cache               internal.Cache
	cacheTTL            time.Duration
	codec               internal.Codec
	hook                internal.Hook
	refs                *refRegistry // keyed state, populated during build phase
	gate                *internal.Gate
	defaultErrorHandler internal.ErrorHandler // nil = use internal.DefaultHandler{}
	defaultBuffer       int                   // 0 = use internal.DefaultBuffer (16)
	defaultKeyTTL       time.Duration         // 0 = no eviction unless overridden per stage

	// done is closed by early-exit stages (Take, TakeWhile) to stop infinite
	// sources (Ticker, Repeatedly, …) without cancelling the run context;
	// which would disrupt downstream stages still draining.
	done       chan struct{}
	signalDone func()

	// drainNotify maps a producer stage ID to its drain entry.
	// Populated during build(); consumers call signalDrain when they exit.
	drainNotify map[int64]*drainEntry

	// segmentByID maps stage ID to segment name. Populated by Segment.Apply's
	// build wrapper at run/Describe time; consulted by runCtx.add to stamp
	// stageMeta.segmentName before appending to rc.metas.
	segmentByID map[int64]string

	// dryRun is true when the run was started with [DryRun]. Effect operators
	// check this and short-circuit fn execution; pure stages run normally.
	dryRun bool

	// effectStats accumulates per-Effect-stage outcome counts during a run.
	// Keyed by the Effect stage's ID. Populated by Effect.build via
	// registerEffectStat; incremented from the Effect goroutine via
	// recordEffectOutcome after each emitted outcome. Read by Runner.Run at
	// shutdown to derive RunOutcome.
	//
	// Map writes (registerEffectStat) all happen during the build phase,
	// before any stage goroutine starts. Counter increments
	// (recordEffectOutcome) use atomic operations and read the entry pointer
	// from a map already populated at build time, so no lock is needed.
	effectStats map[int64]*effectStat

	// devStore, if non-nil, enables per-Segment snapshot capture and replay.
	// Set from cfg.devStore by Runner.Run. Read by Segment.Apply during the
	// build phase to wrap segment output with capture/replay logic.
	devStore DevStore
}

// defaultBufSize returns the run-level default buffer, falling back to
// internal.DefaultBuffer when [WithDefaultBuffer] was not set.
func (rc *runCtx) defaultBufSize() int {
	if rc.defaultBuffer > 0 {
		return rc.defaultBuffer
	}
	return internal.DefaultBuffer
}

// effectiveBufSize returns the buffer size for a stage: the stage's explicit
// Buffer(n) value if set, otherwise the run-level default from [WithDefaultBuffer].
func (rc *runCtx) effectiveBufSize(cfg stageConfig) int {
	if cfg.bufferExplicit {
		return cfg.buffer
	}
	return rc.defaultBufSize()
}

// effectiveKeyTTL returns the key-inactivity TTL for a MapWithKey /
// FlatMapWithKey stage: the stage's explicit WithKeyTTL(d) if set, otherwise
// the run-level default from [WithDefaultKeyTTL] (0 = disabled).
func (rc *runCtx) effectiveKeyTTL(cfg stageConfig) time.Duration {
	if cfg.keyTTLExplicit {
		return cfg.keyTTL
	}
	return rc.defaultKeyTTL
}

func newRunCtx() *runCtx {
	done := make(chan struct{})
	var once sync.Once
	return &runCtx{
		chans:       make(map[int64]any),
		drainNotify: make(map[int64]*drainEntry),
		segmentByID: make(map[int64]string),
		effectStats: make(map[int64]*effectStat),
		refs:        newRefRegistry(),
		done:        done,
		signalDone:  func() { once.Do(func() { close(done) }) },
	}
}

func (rc *runCtx) add(fn stageFunc, meta stageMeta) {
	if name, ok := rc.segmentByID[meta.id]; ok {
		meta.segmentName = name
	}
	rc.stages = append(rc.stages, fn)
	rc.metas = append(rc.metas, meta)
}

func (rc *runCtx) getChan(id int64) any     { return rc.chans[id] }
func (rc *runCtx) setChan(id int64, ch any) { rc.chans[id] = ch }

// initDrainNotify registers a drain entry for producerID with the given
// consumer count. Call once per stage during build(). Pass
// out.consumerCount.Load() where out is the pipeline returned by the
// operator constructor; the count reflects all downstream track() calls
// made before Run was invoked.
//
// Non-fusion stages always have consumerCount == 0 (only fusion-eligible
// pipelines increment consumerCount via track). The clamp to max(1,
// consumerCount) is intentional: a single converted consumer will always
// fire signalDrain once, which is correct for the common single-consumer
// linear case. Multi-consumer fan-out stages require explicit ref counting
// and are tracked in the roadmap follow-on item.
func (rc *runCtx) initDrainNotify(producerID int64, consumerCount int32) {
	e := &drainEntry{ch: make(chan struct{})}
	n := consumerCount
	if n < 1 {
		n = 1
	}
	e.refs.Store(n)
	rc.drainNotify[producerID] = e
}

// initMultiOutputDrainNotify registers ONE shared drainEntry for a fan-out stage.
// All outputIDs map to the same entry with refs = totalConsumers (the sum of
// out[i].consumerCount.Load() across all output pipelines).
// When any consumer calls signalDrain(outputID), the shared counter decrements.
// When it reaches zero the drain channel closes, unblocking the stage's select.
func (rc *runCtx) initMultiOutputDrainNotify(outputIDs []int64, totalConsumers int32) {
	e := &drainEntry{ch: make(chan struct{})}
	n := totalConsumers
	if n < 1 {
		n = 1
	}
	e.refs.Store(n)
	for _, id := range outputIDs {
		rc.drainNotify[id] = e
	}
}

// signalDrain decrements the ref count for producerID. When the count
// reaches zero the drain channel is closed, waking the producer's select.
// Safe to call even if producerID has no registered entry (no-op).
func (rc *runCtx) signalDrain(producerID int64) {
	if e, ok := rc.drainNotify[producerID]; ok {
		if e.refs.Add(-1) == 0 {
			close(e.ch)
		}
	}
}

// drainCh returns the drain-notification channel for id.
// Returns nil when id has no registered entry; a nil channel in a select
// blocks forever, which is the correct fallback for unconverted stages.
func (rc *runCtx) drainCh(id int64) <-chan struct{} {
	if e, ok := rc.drainNotify[id]; ok {
		return e.ch
	}
	return nil
}

// registerEffectStat creates and stores an effectStat entry for an Effect
// stage. Called from Effect.build during the build phase, before any stage
// goroutine starts. Safe without a lock because all build closures run
// sequentially in a single goroutine before Run starts the stage workers.
func (rc *runCtx) registerEffectStat(id int64, name string, required bool) {
	rc.effectStats[id] = &effectStat{name: name, required: required}
}

// recordEffectOutcome increments the success or failure counter for an
// Effect stage. Called from inside the Effect goroutine after each emitted
// outcome. Safe for concurrent use because the entry pointer is read from a
// map that was fully populated during the build phase.
func (rc *runCtx) recordEffectOutcome(id int64, applied bool) {
	s, ok := rc.effectStats[id]
	if !ok {
		return
	}
	if applied {
		s.success.Add(1)
	} else {
		s.failure.Add(1)
	}
}

// recordEffectDeduped increments the per-Effect-stage deduped counter for
// id. Called once per item whose idempotency key matched a previously
// recorded invocation; the corresponding effect function was not called.
func (rc *runCtx) recordEffectDeduped(id int64) {
	if s, ok := rc.effectStats[id]; ok {
		s.deduped.Add(1)
	}
}

// ---------------------------------------------------------------------------
// Pipeline[T]
// ---------------------------------------------------------------------------

// Pipeline[T] is a lazy, reusable pipeline blueprint.
// It describes a computation but allocates no channels until Run is called.
// Each call to Run materialises a fresh, independent channel graph, so the
// same Pipeline[T] value may be run multiple times or used as the input to
// multiple independent operators.
//
// It is an immutable handle; every operator returns a new Pipeline.
// No processing occurs until [Runner.Run] is called.
type Pipeline[T any] struct {
	id   int64
	meta stageMeta // static description (name, kind, inputs, buffer size, …)

	// build is called by Runner.Run to materialise this stage.
	// It recursively calls build on upstream pipelines, allocates a fresh
	// typed channel, registers the stage function into rc, and returns the
	// channel so downstream stages can read from it.
	// build is memoised via rc: if this stage was already built in the current
	// Run, the existing channel is returned immediately without re-registering.
	build func(rc *runCtx) chan T

	// fusionEntry is non-nil for fast-path-eligible Map and Filter stages.
	// A terminal (ForEach) may call this instead of build to compose the entire
	// chain into a single goroutine with zero inter-stage channel hops and zero
	// boxing.  fusionEntry receives the rc and a typed sink function; it
	// recursively composes upstream fusionEntries and returns a single stageFunc
	// that the terminal registers with rc.  The caller must first verify
	// consumerCount == 1 to ensure no other stage is also consuming this pipeline.
	fusionEntry func(rc *runCtx, sink func(context.Context, T) error) stageFunc

	// consumerCount is incremented at construction time by every operator or
	// terminal that consumes this pipeline (via track).  fusionEntry is safe to
	// use only when consumerCount == 1 (single-consumer chain).
	consumerCount atomic.Int32
}

func newPipeline[T any](id int64, meta stageMeta, build func(*runCtx) chan T) *Pipeline[T] {
	return &Pipeline[T]{id: id, meta: meta, build: build}
}

// Describe walks the pipeline DAG and returns a topologically-ordered snapshot
// of every stage (including this one) as []GraphNode, the same shape
// delivered to [GraphHook.OnGraph] during [Runner.Run].
//
// Describe is non-destructive and may be called on any *Pipeline[T], including
// intermediate (non-terminal) pipelines. It does not execute user-supplied
// functions, does not open Store or Cache connections, and does not start
// goroutines. It does allocate the inter-stage channels a Run would use; these
// are garbage-collected when Describe returns.
//
// Describe is safe to call multiple times. Stage IDs are stable across calls
// because they are assigned at pipeline construction time.
func (p *Pipeline[T]) Describe() []internal.GraphNode {
	rc := newRunCtx()
	rc.hook = internal.NoopHook{}
	rc.codec = internal.JSONCodec{}
	_ = p.build(rc)
	return metasToGraphNodes(rc.metas)
}

// ---------------------------------------------------------------------------
// refRegistry: state management stub (populated during a future phase)
// ---------------------------------------------------------------------------

type refRegistry struct {
	mu    sync.RWMutex
	inits map[string]func(internal.Store, internal.Codec) any
	vals  map[string]any
}

func newRefRegistry() *refRegistry {
	return &refRegistry{
		inits: make(map[string]func(internal.Store, internal.Codec) any),
		vals:  make(map[string]any),
	}
}

// register records a factory for a state key. Called during the build phase
// (before Run starts any stage goroutine); uses the write lock because build
// closures for sibling stages may run on different goroutines in future.
func (r *refRegistry) register(name string, factory func(internal.Store, internal.Codec) any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.inits[name]; !ok {
		r.inits[name] = factory
	}
}

// init materialises every registered factory exactly once. Called from Run
// after the build phase completes and before any stage goroutine starts.
func (r *refRegistry) init(store internal.Store, codec internal.Codec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, factory := range r.inits {
		if _, ok := r.vals[name]; !ok {
			r.vals[name] = factory(store, codec)
		}
	}
}

// get returns the materialised value for a key. Safe for concurrent use by
// stage goroutines: init has already populated vals before any stage starts,
// so get is a pure read and only needs an RLock.
func (r *refRegistry) get(name string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vals[name]
}
