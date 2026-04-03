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
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// stageFunc is a goroutine body launched by Run.
// The context originates from Runner.Run and is passed at execution time,
// not at pipeline construction time.
type stageFunc func(ctx context.Context) error

// stageMeta holds introspection data for a single stage (for hooks and inspector).
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
	getChanLen  func() int // returns current channel fill level
	getChanCap  func() int // returns channel capacity
}

// stageList is shared across all Pipelines that originate from the same source graph.
type stageList struct {
	mu     sync.Mutex
	stages []stageFunc
	metas  []stageMeta
	refs   *refRegistry
	idSeq  int
}

func newStageList() *stageList {
	return &stageList{
		refs: &refRegistry{
			inits: make(map[string]func(internal.Store, internal.Codec) any),
			vals:  make(map[string]any),
		},
	}
}

func (sl *stageList) add(fn stageFunc, meta stageMeta) int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	meta.id = sl.idSeq
	sl.idSeq++
	sl.stages = append(sl.stages, fn)
	sl.metas = append(sl.metas, meta)
	return meta.id
}

func (sl *stageList) nextID() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	id := sl.idSeq
	sl.idSeq++
	return id
}

// refRegistry holds state key factories and their resolved values for a pipeline run.
// Will be populated during the state management phase.
type refRegistry struct {
	mu    sync.Mutex
	inits map[string]func(internal.Store, internal.Codec) any
	vals  map[string]any
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

// Pipeline[T] is a lazy data pipeline. Items flow from a source through
// zero or more transformation stages and are consumed by a terminal.
// It is an immutable handle — every operation returns a new Pipeline.
// No processing occurs until [Runner.Run] is called.
type Pipeline[T any] struct {
	ch   chan T       // typed output channel of this stage
	sl   *stageList  // shared with all upstream stages
	id   int         // this stage's ID in sl.metas
	port int         // output port (0 for single-output stages)
}

// newPipeline creates a new Pipeline connected to a typed channel.
func newPipeline[T any](ch chan T, sl *stageList, id int) *Pipeline[T] {
	return &Pipeline[T]{ch: ch, sl: sl, id: id}
}

// combineStageLists merges all unique stageLists into the first one and returns
// the primary. If all inputs already share the same stageList, returns it directly.
// This is used by multi-input operators (Merge, Zip, CombineLatest, etc.) so that
// pipelines from independent sources can be connected.
func combineStageLists(stageLists ...*stageList) *stageList {
	if len(stageLists) == 0 {
		return newStageList()
	}
	primary := stageLists[0]
	seen := map[*stageList]bool{primary: true}
	for _, sl := range stageLists[1:] {
		if seen[sl] {
			continue // already merged
		}
		seen[sl] = true
		// combineStageLists is only called at pipeline construction time (single-threaded),
		// so we don't need lock ordering — just lock sequentially.
		primary.mu.Lock()
		sl.mu.Lock()
		primary.stages = append(primary.stages, sl.stages...)
		primary.metas = append(primary.metas, sl.metas...)
		sl.mu.Unlock()
		primary.mu.Unlock()
	}
	return primary
}

