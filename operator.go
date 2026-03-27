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
//
// Broadcast panics if n <= 0.
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
// (e.g., branches from the same [Partition] or [Broadcast]).
//
// Merge panics if no pipelines are provided or if the pipelines do not
// share the same graph.
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
// Scan / GroupBy
// ---------------------------------------------------------------------------

// Scan applies fn to an accumulator and each item, emitting the new
// accumulator value after every item. initial seeds the accumulator before
// the first item arrives.
//
// The output type S may differ from the input type T:
//
//	// Running total (int → float64)
//	kitsune.Scan(prices, 0.0, func(sum float64, p Price) float64 {
//	    return sum + p.Amount
//	})
//
//	// Sliding max
//	kitsune.Scan(readings, math.MinInt, func(max, v int) int {
//	    if v > max { return v }
//	    return max
//	})
//
// Scan is inherently sequential — each output depends on the previous
// accumulator, so it always runs as a single worker regardless of any
// [Concurrency] option applied downstream.
func Scan[T, S any](p *Pipeline[T], initial S, fn func(S, T) S) *Pipeline[S] {
	state := initial
	return Map(p, func(_ context.Context, item T) (S, error) {
		state = fn(state, item)
		return state, nil
	})
}

// GroupBy collects all items into a map keyed by the result of key, then
// emits the complete map as a single item.
//
//	groups, _ := kitsune.GroupBy(
//	    kitsune.FromSlice(events),
//	    func(e Event) string { return e.Type },
//	).Collect(ctx)
//	// groups[0] is a map[string][]Event
//
// Because GroupBy must see every item before emitting, it buffers the entire
// stream in memory. Only use it on bounded (finite) pipelines.
//
// To process each group independently, follow GroupBy with a [Map] or
// [FlatMap] that iterates over the map entries.
func GroupBy[T any, K comparable](p *Pipeline[T], key func(T) K) *Pipeline[map[K][]T] {
	return Map(Batch(p, math.MaxInt), func(_ context.Context, items []T) (map[K][]T, error) {
		m := make(map[K][]T)
		for _, item := range items {
			k := key(item)
			m[k] = append(m[k], item)
		}
		return m, nil
	})
}

// ---------------------------------------------------------------------------
// Distinct / DistinctBy
// ---------------------------------------------------------------------------

// Distinct drops duplicate items, keeping only the first occurrence of each
// value. T must be comparable (numbers, strings, structs with comparable fields).
//
//	kitsune.Distinct(kitsune.FromSlice([]int{1, 2, 1, 3, 2}))
//	// → 1 2 3
//
// Distinct keeps a set of all seen values in memory. For non-comparable types,
// or when cardinality is high and you need distributed dedup, use [DistinctBy]
// or [Dedupe] with a [DedupSet] instead.
func Distinct[T comparable](p *Pipeline[T]) *Pipeline[T] {
	seen := make(map[T]struct{})
	return p.Filter(func(item T) bool {
		if _, ok := seen[item]; ok {
			return false
		}
		seen[item] = struct{}{}
		return true
	})
}

// DistinctBy drops items whose key has already been seen, keeping only the
// first occurrence per key. Use this when T is not comparable or when you
// need to deduplicate by a derived field.
//
//	kitsune.DistinctBy(events, func(e Event) string { return e.ID })
//
// Unlike [Dedupe], DistinctBy uses an in-process map with no external
// storage, so it is not suitable for distributed pipelines. For distributed
// deduplication use [Dedupe] with a Redis-backed [DedupSet].
//
// DistinctBy buffers all seen keys in memory — use on bounded streams or
// streams with bounded cardinality.
func DistinctBy[T any](p *Pipeline[T], key func(T) string) *Pipeline[T] {
	seen := make(map[string]struct{})
	return p.Filter(func(item T) bool {
		k := key(item)
		if _, ok := seen[k]; ok {
			return false
		}
		seen[k] = struct{}{}
		return true
	})
}

// ---------------------------------------------------------------------------
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

// TakeWhile emits items as long as fn returns true. The first item for which
// fn returns false stops the pipeline — no further items are processed,
// including that item itself.
//
//	// Emit log lines until a "STOP" sentinel is seen.
//	kitsune.TakeWhile(lines, func(s string) bool { return s != "STOP" })
//
// Like [Pipeline.Take], TakeWhile signals upstream sources to stop producing,
// so it is safe to use with infinite generators. [DropWhile] is its complement.
func TakeWhile[T any](p *Pipeline[T], fn func(T) bool) *Pipeline[T] {
	wrapped := func(in any) bool { return fn(in.(T)) }
	id := p.g.AddNode(&engine.Node{
		Kind:   engine.TakeWhile,
		Fn:     wrapped,
		Inputs: []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer: engine.DefaultBuffer,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// DropWhile suppresses items as long as fn returns true. Once fn returns
// false, that item and all subsequent items are emitted regardless of what
// fn would return for them — the predicate is not re-evaluated after the
// first false.
//
//	// Skip header lines that start with '#', then pass everything else.
//	kitsune.DropWhile(lines, func(s string) bool { return strings.HasPrefix(s, "#") })
//
// [TakeWhile] is the complement: it emits items while the predicate holds.
func DropWhile[T any](p *Pipeline[T], fn func(T) bool) *Pipeline[T] {
	dropping := true
	return p.Filter(func(item T) bool {
		if dropping && fn(item) {
			return false
		}
		dropping = false
		return true
	})
}

// ---------------------------------------------------------------------------
// Zip
// ---------------------------------------------------------------------------

// Pair holds one item from each of two pipelines joined by [Zip].
// First is from the left pipeline; Second is from the right pipeline.
type Pair[A, B any] struct {
	First  A
	Second B
}

// Zip pairs items from two same-graph pipelines by position, emitting a
// [Pair] for each corresponding pair of items. Output stops as soon as
// either input closes — the shorter pipeline determines the output length.
//
// A typical use is to track originals alongside their transformed versions:
//
//	branches := kitsune.Broadcast[Record](source, 2)
//	enriched := kitsune.Map(branches[1], enrich)
//	pairs := kitsune.Zip(branches[0], enriched)
//	// Each Pair carries (original, enriched) for side-by-side comparison.
//
// Both pipelines must share the same graph (i.e., they must originate from
// the same source or a [Partition] / [Broadcast] of the same source).
// Zip panics if the pipelines do not share the same graph.
//
// Note: Zip reads from each input sequentially (first A, then B). If the two
// pipelines produce at very different rates, the faster one will accumulate
// items in its buffer while Zip waits for the slower one. Size those buffers
// accordingly using [Buffer].
func Zip[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	if a.g != b.g {
		panic("kitsune: Zip requires both pipelines to share the same pipeline graph")
	}
	convert := func(va, vb any) any {
		return Pair[A, B]{First: va.(A), Second: vb.(B)}
	}
	id := a.g.AddNode(&engine.Node{
		Kind:       engine.ZipNode,
		Inputs:     []engine.InputRef{{Node: a.node, Port: a.port}, {Node: b.node, Port: b.port}},
		Buffer:     engine.DefaultBuffer,
		ZipConvert: convert,
	})
	return &Pipeline[Pair[A, B]]{g: a.g, node: id}
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
