package kitsune

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// ---------------------------------------------------------------------------
// Type-changing transforms (free functions — T may change)
// ---------------------------------------------------------------------------

// Map applies a 1:1 transformation, potentially changing the item type.
func Map[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[O] {
	wrapped := func(ctx context.Context, in any) (any, error) {
		return fn(ctx, in.(I))
	}
	n := newNode(engine.Map, wrapped, p, opts)
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// FlatMap applies a 1:N transformation — each input produces zero or more outputs.
func FlatMap[I, O any](p *Pipeline[I], fn func(context.Context, I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	wrapped := func(ctx context.Context, in any) ([]any, error) {
		results, err := fn(ctx, in.(I))
		if err != nil {
			return nil, err
		}
		out := make([]any, len(results))
		for i, r := range results {
			out[i] = r
		}
		return out, nil
	}
	n := newNode(engine.FlatMap, wrapped, p, opts)
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// Batch collects items into slices of up to size elements.
// Use [BatchTimeout] to flush partial batches after a duration.
func Batch[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T] {
	convert := func(items []any) any {
		result := make([]T, len(items))
		for i, item := range items {
			result[i] = item.(T)
		}
		return result
	}
	cfg := buildStageConfig(opts)
	n := &engine.Node{
		Kind:         engine.Batch,
		Name:         cfg.name,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:       cfg.buffer,
		BatchSize:    size,
		BatchTimeout: cfg.batchTimeout.Nanoseconds(),
		BatchConvert: convert,
	}
	id := p.g.AddNode(n)
	return &Pipeline[[]T]{g: p.g, node: id}
}

// Unbatch flattens a Pipeline of slices back into individual items.
// It is the inverse of [Batch].
func Unbatch[T any](p *Pipeline[[]T]) *Pipeline[T] {
	wrapped := func(_ context.Context, in any) ([]any, error) {
		slice := in.([]T)
		out := make([]any, len(slice))
		for i, v := range slice {
			out[i] = v
		}
		return out, nil
	}
	n := &engine.Node{
		Kind:         engine.FlatMap,
		Fn:           wrapped,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:       engine.DefaultBuffer,
		ErrorHandler: engine.DefaultHandler{},
		Concurrency:  1,
	}
	id := p.g.AddNode(n)
	return &Pipeline[T]{g: p.g, node: id}
}

// ---------------------------------------------------------------------------
// Type-preserving transforms (methods — T stays the same)
// ---------------------------------------------------------------------------

// Filter keeps only items for which the predicate returns true.
func (p *Pipeline[T]) Filter(fn func(T) bool) *Pipeline[T] {
	wrapped := func(in any) bool { return fn(in.(T)) }
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.Filter,
		Fn:     wrapped,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// Tap calls fn for each item as a side effect without modifying the stream.
// Useful for logging or metrics collection.
func (p *Pipeline[T]) Tap(fn func(T)) *Pipeline[T] {
	wrapped := func(in any) { fn(in.(T)) }
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.Tap,
		Fn:     wrapped,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// Take emits the first n items, then signals completion.
func (p *Pipeline[T]) Take(n int) *Pipeline[T] {
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.Take,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
		TakeN:  n,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// Through applies a reusable, type-preserving pipeline fragment.
//
//	func Validate(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
//	    return p.Filter(isValid).Tap(logOrder)
//	}
//	orders.Through(Validate)
func (p *Pipeline[T]) Through(fn func(*Pipeline[T]) *Pipeline[T]) *Pipeline[T] {
	return fn(p)
}

// ---------------------------------------------------------------------------
// Fan-out / fan-in
// ---------------------------------------------------------------------------

// Partition splits a pipeline into two based on a predicate.
// Items where fn returns true go to match; the rest go to rest.
// Every item goes to exactly one output.
func Partition[T any](p *Pipeline[T], fn func(T) bool) (match, rest *Pipeline[T]) {
	wrapped := func(in any) bool { return fn(in.(T)) }
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.Partition,
		Fn:     wrapped,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: p.g, node: id, port: 0},
		&Pipeline[T]{g: p.g, node: id, port: 1}
}

// Broadcast copies every item to all N output pipelines.
// Unlike [Partition] (which routes each item to one output), Broadcast
// sends every item to every consumer.
func Broadcast[T any](p *Pipeline[T], n int) []*Pipeline[T] {
	if n <= 0 {
		panic("kitsune: Broadcast requires n > 0")
	}
	id := p.g.AddNode(&engine.Node{
		Kind:       engine.BroadcastNode,
		Inputs:     []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:     engine.DefaultBuffer,
		BroadcastN: n,
	})
	out := make([]*Pipeline[T], n)
	for i := range n {
		out[i] = &Pipeline[T]{g: p.g, node: id, port: i}
	}
	return out
}

// Merge combines multiple pipelines of the same type into one.
// All input pipelines must share the same pipeline graph
// (e.g., branches from the same [Partition]).
func Merge[T any](ps ...*Pipeline[T]) *Pipeline[T] {
	if len(ps) == 0 {
		panic("kitsune: Merge requires at least one pipeline")
	}
	if len(ps) == 1 {
		return ps[0]
	}
	g := ps[0].g
	inputs := make([]engine.InputRef, len(ps))
	for i, p := range ps {
		if p.g != g {
			panic("kitsune: Merge requires all pipelines to share the same pipeline graph")
		}
		inputs[i] = engine.InputRef{Node: p.node, Port: p.port}
	}
	id := g.AddNode(&engine.Node{
		Kind:   engine.Merge,
		Inputs: inputs,
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: g, node: id}
}

// ---------------------------------------------------------------------------
// Window
// ---------------------------------------------------------------------------

// Window collects items into slices based on time. Every duration d, the
// accumulated items are flushed as a batch. This is a convenience wrapper
// around [Batch] with an effectively unlimited size and a mandatory timeout.
func Window[T any](p *Pipeline[T], d time.Duration) *Pipeline[[]T] {
	return Batch(p, math.MaxInt, BatchTimeout(d))
}

// ---------------------------------------------------------------------------
// Dedupe / CachedMap
// ---------------------------------------------------------------------------

// Dedupe drops items whose key has already been seen.
// The [DedupSet] tracks seen keys — use [MemoryDedupSet] for in-process
// deduplication or a Redis-backed implementation for distributed pipelines.
func Dedupe[T any](p *Pipeline[T], key func(T) string, set DedupSet) *Pipeline[T] {
	wrapped := func(in any) bool {
		item := in.(T)
		k := key(item)
		seen, err := set.Contains(context.Background(), k)
		if err != nil || seen {
			return false
		}
		_ = set.Add(context.Background(), k)
		return true
	}
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.Filter,
		Fn:     wrapped,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// CachedMap is like [Map] but checks the cache before calling fn.
// On cache hit, the cached result is returned without calling fn.
// On cache miss, fn is called and the result is cached with the given TTL.
func CachedMap[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), key func(I) string, cache Cache, ttl time.Duration, opts ...StageOption) *Pipeline[O] {
	wrapped := func(ctx context.Context, in any) (any, error) {
		item := in.(I)
		k := key(item)

		// Check cache.
		if data, ok, err := cache.Get(ctx, k); err == nil && ok {
			var cached O
			if err := json.Unmarshal(data, &cached); err == nil {
				return cached, nil
			}
		}

		// Cache miss — compute.
		result, err := fn(ctx, item)
		if err != nil {
			return nil, err
		}

		// Store in cache (best-effort).
		if data, err := json.Marshal(result); err == nil {
			_ = cache.Set(ctx, k, data, ttl)
		}

		return result, nil
	}
	n := newNode(engine.Map, wrapped, p, opts)
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// Lift wraps a context-free function for use with [Map] and [FlatMap].
//
//	numbers := kitsune.Map(lines, kitsune.Lift(strconv.Atoi))
func Lift[I, O any](fn func(I) (O, error)) func(context.Context, I) (O, error) {
	return func(_ context.Context, in I) (O, error) {
		return fn(in)
	}
}
