package kitsune

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// Pool — type-safe object pooling
// ---------------------------------------------------------------------------

// Pool is a type-safe object pool backed by [sync.Pool]. Use it to reduce
// allocator pressure in high-throughput pipelines by reusing output objects
// instead of allocating new ones on every item.
//
//	type Buffer struct{ Data []byte }
//	pool := kitsune.NewPool(func() *Buffer {
//	    return &Buffer{Data: make([]byte, 0, 4096)}
//	})
type Pool[T any] struct {
	p sync.Pool
}

// NewPool creates a Pool with the given factory function.
// factory is called whenever the pool is empty and a new object is needed.
func NewPool[T any](factory func() T) *Pool[T] {
	return &Pool[T]{
		p: sync.Pool{New: func() any { return factory() }},
	}
}

// Get retrieves an object from the pool, or allocates a fresh one via the
// factory if the pool is empty.
func (p *Pool[T]) Get() T {
	return p.p.Get().(T)
}

// Put returns an object to the pool for future reuse.
func (p *Pool[T]) Put(v T) {
	p.p.Put(v)
}

// ---------------------------------------------------------------------------
// Pooled — pool-aware item wrapper
// ---------------------------------------------------------------------------

// Pooled wraps a pipeline item with a reference to its source pool.
// Call [Pooled.Release] after consuming the value to return it to the pool.
//
// Pooled items are produced by [MapPooled]. Downstream stages work with
// p.Value; call p.Release() in the terminal stage (ForEach, or after Collect).
type Pooled[T any] struct {
	// Value is the pipeline item. Do not retain a reference to Value after
	// calling Release.
	Value T
	pool  *Pool[T]
}

// Release returns the item to the pool. It is safe to call Release multiple
// times — subsequent calls after the first are no-ops.
func (p *Pooled[T]) Release() {
	if p.pool != nil {
		p.pool.Put(p.Value)
		p.pool = nil
	}
}

// ReleaseAll releases every item in a slice of Pooled values back to their
// pool. Call this after processing the results of [Collect] on a pipeline
// that produces [Pooled] items.
//
//	results, _ := mapped.Collect(ctx)
//	defer kitsune.ReleaseAll(results)
//	for _, r := range results {
//	    process(r.Value)
//	}
func ReleaseAll[T any](items []Pooled[T]) {
	for i := range items {
		items[i].Release()
	}
}

// ---------------------------------------------------------------------------
// MapPooled — pool-integrated transform
// ---------------------------------------------------------------------------

// MapPooled applies fn to each item using a pre-allocated output object from
// pool. The result is wrapped in [Pooled][O] so the caller can [Pooled.Release]
// it after use, returning the object to the pool.
//
// fn receives the input item and a pre-allocated output object obtained from
// pool. It should populate and return that object (or a new one). On error the
// pre-allocated object is returned to the pool automatically.
//
//	pool := kitsune.NewPool(func() *Result { return new(Result) })
//	out := kitsune.MapPooled(input, pool,
//	    func(ctx context.Context, item Input, r *Result) (*Result, error) {
//	        r.Field = transform(item)
//	        return r, nil
//	    },
//	)
//	results, _ := out.Collect(ctx)
//	defer kitsune.ReleaseAll(results)
func MapPooled[I, O any](
	p *Pipeline[I],
	pool *Pool[O],
	fn func(context.Context, I, O) (O, error),
	opts ...StageOption,
) *Pipeline[Pooled[O]] {
	return Map(p, func(ctx context.Context, item I) (Pooled[O], error) {
		obj := pool.Get()
		result, err := fn(ctx, item, obj)
		if err != nil {
			pool.Put(obj) // return on error to avoid leak
			return Pooled[O]{}, err
		}
		return Pooled[O]{Value: result, pool: pool}, nil
	}, opts...)
}
