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
	id          int
	name        string
	kind        string
	inputs      []int
	concurrency int
	buffer      int
	overflow    internal.Overflow
	batchSize   int
	timeout     time.Duration
	hasRetry    bool
	hasSuperv   bool
	getChanLen  func() int
	getChanCap  func() int
}

// ---------------------------------------------------------------------------
// Global stage-ID counter
// ---------------------------------------------------------------------------

var globalIDSeq int64

// nextPipelineID returns a process-unique ID for each constructed stage.
// IDs are used for graph visualisation and runCtx memoisation.
func nextPipelineID() int {
	return int(atomic.AddInt64(&globalIDSeq, 1))
}

// ---------------------------------------------------------------------------
// runCtx — per-Run execution context
// ---------------------------------------------------------------------------

// runCtx is created fresh on every Runner.Run call.
// As build functions are called recursively from the terminal back to sources,
// each stage registers its stageFunc here and its output channel is memoised
// by stage ID so that shared upstream stages are only built once per run.
type runCtx struct {
	stages   []stageFunc
	metas    []stageMeta
	chans    map[int]any // stage ID → chan T (type-erased for storage)
	cache    internal.Cache
	cacheTTL time.Duration
	codec    internal.Codec
	hook     internal.Hook
	refs     *refRegistry // keyed state, populated during build phase

	// done is closed by early-exit stages (Take, TakeWhile) to stop infinite
	// sources (Ticker, Interval, Repeatedly, …) without cancelling the run
	// context — which would disrupt downstream stages still draining.
	done       chan struct{}
	signalDone func()
}

func newRunCtx() *runCtx {
	done := make(chan struct{})
	var once sync.Once
	return &runCtx{
		chans:      make(map[int]any),
		refs:       newRefRegistry(),
		done:       done,
		signalDone: func() { once.Do(func() { close(done) }) },
	}
}

func (rc *runCtx) add(fn stageFunc, meta stageMeta) {
	rc.stages = append(rc.stages, fn)
	rc.metas = append(rc.metas, meta)
}

func (rc *runCtx) getChan(id int) any     { return rc.chans[id] }
func (rc *runCtx) setChan(id int, ch any) { rc.chans[id] = ch }

// ---------------------------------------------------------------------------
// Pipeline[T]
// ---------------------------------------------------------------------------

// Pipeline[T] is a lazy, reusable pipeline blueprint.
// It describes a computation but allocates no channels until Run is called.
// Each call to Run materialises a fresh, independent channel graph, so the
// same Pipeline[T] value may be run multiple times or used as the input to
// multiple independent operators.
//
// It is an immutable handle — every operator returns a new Pipeline.
// No processing occurs until [Runner.Run] is called.
type Pipeline[T any] struct {
	id   int
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

func newPipeline[T any](id int, meta stageMeta, build func(*runCtx) chan T) *Pipeline[T] {
	return &Pipeline[T]{id: id, meta: meta, build: build}
}

// ---------------------------------------------------------------------------
// refRegistry — state management stub (populated during a future phase)
// ---------------------------------------------------------------------------

type refRegistry struct {
	mu    sync.Mutex
	inits map[string]func(internal.Store, internal.Codec) any
	vals  map[string]any
}

func newRefRegistry() *refRegistry {
	return &refRegistry{
		inits: make(map[string]func(internal.Store, internal.Codec) any),
		vals:  make(map[string]any),
	}
}

func (r *refRegistry) register(name string, factory func(internal.Store, internal.Codec) any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.inits[name]; !ok {
		r.inits[name] = factory
	}
}

func (r *refRegistry) init(store internal.Store, codec internal.Codec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, factory := range r.inits {
		if _, ok := r.vals[name]; !ok {
			r.vals[name] = factory(store, codec)
		}
	}
}

func (r *refRegistry) get(name string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vals[name]
}
