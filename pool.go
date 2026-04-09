package kitsune

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// Pool / Pooled / MapPooled
// ---------------------------------------------------------------------------

// Pool is a generic wrapper around [sync.Pool].
// Use [NewPool] to create one and [MapPooled] to use it as a pipeline stage.
type Pool[T any] struct {
	p sync.Pool
}

// NewPool returns a Pool backed by newFn. newFn is called whenever the pool
// has no free objects to return.
func NewPool[T any](newFn func() T) *Pool[T] {
	p := &Pool[T]{}
	p.p.New = func() any {
		v := newFn()
		return &Pooled[T]{Value: v, pool: p}
	}
	return p
}

// Get acquires an object from the pool. Call [Pooled.Release] when done.
func (p *Pool[T]) Get() *Pooled[T] {
	return p.p.Get().(*Pooled[T])
}

// Put returns item to the pool. Equivalent to calling [Pooled.Release] on the
// item directly. After Put, the item's Value field must not be accessed.
func (p *Pool[T]) Put(item *Pooled[T]) {
	p.put(item)
}

func (p *Pool[T]) put(item *Pooled[T]) {
	p.p.Put(item)
}

// Warmup pre-populates the pool with n objects by calling the factory function
// n times and immediately returning them. This reduces first-use latency for
// latency-sensitive start-up paths. Warmup is best-effort: sync.Pool may evict
// objects at any time (e.g. on GC). No-op for n <= 0.
func (p *Pool[T]) Warmup(n int) {
	if n <= 0 {
		return
	}
	items := make([]*Pooled[T], n)
	for i := range items {
		items[i] = p.Get()
	}
	for _, item := range items {
		p.put(item)
	}
}

// Pooled wraps a value obtained from a [Pool].
// Always call [Release] when finished so the value returns to the pool.
type Pooled[T any] struct {
	Value T
	pool  *Pool[T]
}

// Release returns the value to its originating pool. After Release, the
// Value field must not be read or written by the caller.
func (w *Pooled[T]) Release() {
	if w.pool != nil {
		w.pool.put(w)
	}
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
