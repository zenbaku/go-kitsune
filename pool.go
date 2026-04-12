package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Pool / Pooled / MapPooled
// ---------------------------------------------------------------------------

// Pool is a size-unbounded object pool backed by a mutex-protected stack.
// Objects are returned in LIFO order so recently-used items (likely still in
// CPU cache) are preferred. Use [NewPool] to create one and [MapPooled] to use
// it as a pipeline stage.
type Pool[T any] struct {
	mu    sync.Mutex
	stack []*Pooled[T]
	newFn func() T
}

// NewPool returns a Pool backed by newFn. newFn is called whenever the pool
// has no free objects to return.
func NewPool[T any](newFn func() T) *Pool[T] {
	return &Pool[T]{newFn: newFn}
}

// Get acquires an object from the pool. Call [Pooled.Release] when done.
//
// Get acquires a sync.Mutex on every call to access the internal stack. With
// Concurrency(n), all n worker goroutines call Get in their hot loop and
// serialise on that lock. At low concurrency (n ≤ 4) contention is rarely
// measurable. At high concurrency (n ≥ 8), profile to verify the pool lock is
// not capping throughput; increasing fn work size or using Warmup to keep the
// stack populated can reduce allocation jitter.
//
// If per-P cache behaviour is more important than LIFO ordering or eviction
// guarantees, sync.Pool is an alternative, though its objects may be evicted at
// GC time.
func (p *Pool[T]) Get() *Pooled[T] {
	p.mu.Lock()
	if n := len(p.stack); n > 0 {
		item := p.stack[n-1]
		p.stack = p.stack[:n-1]
		p.mu.Unlock()
		item.released.Store(false)
		return item
	}
	p.mu.Unlock()
	v := p.newFn()
	return &Pooled[T]{Value: v, pool: p}
}

// Put returns item to the pool. Equivalent to calling [Pooled.Release] on the
// item directly. After Put, the item's Value field must not be accessed.
func (p *Pool[T]) Put(item *Pooled[T]) {
	item.Release()
}

func (p *Pool[T]) put(item *Pooled[T]) {
	p.mu.Lock()
	p.stack = append(p.stack, item)
	p.mu.Unlock()
}

// Warmup pre-populates the pool with n objects by calling the factory function
// n times and immediately returning them. This reduces first-use latency for
// latency-sensitive start-up paths. Pre-populated objects are never evicted; they
// remain in the pool until retrieved by Get. No-op for n <= 0.
func (p *Pool[T]) Warmup(n int) {
	if n <= 0 {
		return
	}
	items := make([]*Pooled[T], n)
	for i := range items {
		items[i] = p.Get()
	}
	for _, item := range items {
		p.put(item) // bypass Release: these items were never handed to a caller
	}
}

// Pooled wraps a value obtained from a [Pool].
// Always call [Release] when finished so the value returns to the pool.
//
// WARNING: do not read or write Value after calling Release. The pool may hand
// the underlying object to another goroutine immediately, yielding silent data
// corruption. Use [Pooled.MustValue] for a guarded read that panics on
// use-after-release.
type Pooled[T any] struct {
	// Value is the pooled object. It must not be read or written after Release
	// is called. Use MustValue for a release-checked accessor.
	Value    T
	pool     *Pool[T]
	released atomic.Bool
}

// Release returns the value to its originating pool. After Release, the
// Value field must not be read or written by the caller.
//
// Calling Release more than once on the same item panics: double-release
// indicates a use-after-release bug in the caller.
func (w *Pooled[T]) Release() {
	if !w.released.CompareAndSwap(false, true) {
		panic("kitsune: double Release on *Pooled[T]; indicates a use-after-release bug")
	}
	if w.pool != nil {
		w.pool.put(w)
	}
}

// MustValue returns the pooled value, panicking if Release has already been
// called. Use this instead of Value when use-after-release detection is
// preferred over zero-overhead access.
func (w *Pooled[T]) MustValue() T {
	if w.released.Load() {
		panic("kitsune: read of Pooled.Value after Release")
	}
	return w.Value
}

// ReleaseAll releases a slice of pooled items back to their respective pools.
func ReleaseAll[T any](items []*Pooled[T]) {
	for _, item := range items {
		item.Release()
	}
}

// MapPooled transforms each item from p using fn, acquiring an output slot
// from pool before each call and emitting the filled [Pooled] wrapper.
//
// fn receives a pre-acquired *Pooled[O]; it should write the computed result
// into pooled.Value. If fn returns an error, the pooled slot is released back
// to the pool automatically.
//
// Downstream code must call [Pooled.Release] on each received item when done,
// or use [ReleaseAll] for batches. Failing to release leaks objects from the pool.
//
// Concurrency note: [Pool.Get] acquires a sync.Mutex on every call. With
// Concurrency(n) > 1, all n workers call Get in parallel and serialise on that
// lock. At low concurrency (n ≤ 4) this is rarely a bottleneck. At high
// concurrency (n ≥ 8), profile to confirm the pool lock is not capping
// throughput. As a mitigation, use [Pool.Warmup] to pre-populate the pool
// (reducing allocation jitter) and keep fn's work coarse enough that per-item
// pool overhead is a small fraction of total processing cost.
//
//	pool := kitsune.NewPool(func() []byte { return make([]byte, 0, 4096) })
//	encoded := kitsune.MapPooled(events, pool,
//	    func(ctx context.Context, e Event, out *kitsune.Pooled[[]byte]) error {
//	        out.Value = appendJSON(out.Value[:0], e)
//	        return nil
//	    })
func MapPooled[I, O any](p *Pipeline[I], pool *Pool[O], fn func(context.Context, I, *Pooled[O]) error, opts ...StageOption) *Pipeline[*Pooled[O]] {
	wrapped := func(ctx context.Context, item I) (*Pooled[O], error) {
		slot := pool.Get()
		if err := fn(ctx, item, slot); err != nil {
			slot.Release()
			return nil, err
		}
		return slot, nil
	}
	opts = append([]StageOption{WithName("map_pooled")}, opts...)
	return Map(p, wrapped, opts...)
}
