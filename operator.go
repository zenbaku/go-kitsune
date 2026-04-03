package kitsune

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

// ---------------------------------------------------------------------------
// Type-changing transforms (free functions — T may change)
// ---------------------------------------------------------------------------

// Map applies a 1:1 transformation, potentially changing the item type.
// Add the [CacheBy] stage option to skip fn on cache hits, or [Timeout] to
// bound per-item execution time:
//
//	kitsune.Map(p, fetchUser, kitsune.CacheBy(func(e Event) string { return e.UserID }))
//	kitsune.Map(p, fetchUser, kitsune.Timeout(500*time.Millisecond))
func Map[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	if cfg.timeout > 0 {
		d := cfg.timeout
		inner := fn
		fn = func(ctx context.Context, item I) (O, error) {
			tCtx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return inner(tCtx, item)
		}
	}
	wrapped := func(ctx context.Context, in any) (any, error) {
		return fn(ctx, in.(I))
	}
	n := newNode(engine.Map, wrapped, p, opts)
	if cc := cfg.cacheConfig; cc != nil {
		keyFn := cc.keyFn.(func(I) string)
		explicitCache := cc.cache
		explicitTTL := cc.ttl
		n.CacheWrapFn = func(defaultCache Cache, defaultTTL time.Duration, codec engine.Codec) any {
			c := explicitCache
			if c == nil {
				c = defaultCache
			}
			ttl := explicitTTL
			if ttl == 0 {
				ttl = defaultTTL
			}
			if c == nil {
				return nil // no cache available; passthrough
			}
			return func(ctx context.Context, in any) (any, error) {
				item := in.(I)
				k := keyFn(item)
				if data, ok, err := c.Get(ctx, k); err == nil && ok {
					var cached O
					if err := codec.Unmarshal(data, &cached); err == nil {
						return cached, nil
					}
				}
				result, err := fn(ctx, item)
				if err != nil {
					return nil, err
				}
				if data, err := codec.Marshal(result); err == nil {
					_ = c.Set(ctx, k, data, ttl)
				}
				return result, nil
			}
		}
	}
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// FlatMap applies a 1:N transformation — each input produces zero or more
// outputs by calling yield. Return an error to abort processing.
// Use [Timeout] to bound per-item execution time.
func FlatMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	if cfg.timeout > 0 {
		d := cfg.timeout
		inner := fn
		fn = func(ctx context.Context, item I, yield func(O) error) error {
			tCtx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return inner(tCtx, item, yield)
		}
	}
	wrapped := func(ctx context.Context, in any, yield func(any) error) error {
		return fn(ctx, in.(I), func(out O) error {
			return yield(out)
		})
	}
	n := newNode(engine.FlatMap, wrapped, p, opts)
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// ConcatMap applies a 1:N transformation sequentially — each input is fully
// expanded before the next is processed. This is equivalent to [FlatMap] with
// [Concurrency](1) (which is the default), and is provided as a named alias
// for users coming from reactive-streams terminology.
//
// For concurrent expansion use [FlatMap] with [Concurrency](n).
func ConcatMap[I, O any](p *Pipeline[I], fn func(context.Context, I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	// Append Concurrency(1) last so it overrides any user-supplied Concurrency.
	opts = append(opts, Concurrency(1))
	return FlatMap(p, fn, opts...)
}

// SwitchMap applies fn to each item from p, starting a new inner pipeline for
// each item and cancelling the previously active inner pipeline. Only the latest
// inner pipeline's output reaches the output pipeline; superseded pipelines are
// cancelled as soon as the next upstream item arrives.
//
// SwitchMap is the "latest wins" counterpart to [ConcatMap] ("all sequential")
// and [FlatMap] ("all concurrent"). Use it for search-as-you-type, live queries,
// and any flow where a new request obsoletes the previous one.
func SwitchMap[I, O any](p *Pipeline[I], fn func(ctx context.Context, item I, yield func(O) error) error, opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	if cfg.timeout > 0 {
		d := cfg.timeout
		inner := fn
		fn = func(ctx context.Context, item I, yield func(O) error) error {
			tCtx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return inner(tCtx, item, yield)
		}
	}
	wrapped := func(ctx context.Context, in any, yield func(any) error) error {
		return fn(ctx, in.(I), func(out O) error {
			return yield(out)
		})
	}
	n := newNode(engine.SwitchMapNode, wrapped, p, opts)
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id}
}

// ExhaustMap applies fn to each item from p but ignores new upstream items
// while the current inner pipeline is still active. Only the first item wins;
// all items arriving while the inner pipeline runs are dropped.
//
// ExhaustMap is the "first wins" counterpart to [SwitchMap] ("latest wins").
// Use it for form submissions, idempotent API calls, and debounced writes where
// only the first trigger should be processed until completion.
func ExhaustMap[I, O any](p *Pipeline[I], fn func(ctx context.Context, item I, yield func(O) error) error, opts ...StageOption) *Pipeline[O] {
	cfg := buildStageConfig(opts)
	if cfg.timeout > 0 {
		d := cfg.timeout
		inner := fn
		fn = func(ctx context.Context, item I, yield func(O) error) error {
			tCtx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return inner(tCtx, item, yield)
		}
	}
	wrapped := func(ctx context.Context, in any, yield func(any) error) error {
		return fn(ctx, in.(I), func(out O) error {
			return yield(out)
		})
	}
	n := newNode(engine.ExhaustMapNode, wrapped, p, opts)
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
		Clock:        cfg.clock,
	}
	id := p.g.AddNode(n)
	return &Pipeline[[]T]{g: p.g, node: id}
}

// Unbatch flattens a Pipeline of slices back into individual items.
// It is the inverse of [Batch].
func Unbatch[T any](p *Pipeline[[]T]) *Pipeline[T] {
	wrapped := func(_ context.Context, in any, yield func(any) error) error {
		for _, v := range in.([]T) {
			if err := yield(v); err != nil {
				return err
			}
		}
		return nil
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
// Merge combines multiple pipelines of the same type into a single stream.
// Items arrive in the order the underlying goroutines produce them.
//
// Pipelines may come from the same graph or from completely independent graphs —
// both cases are handled automatically. When all inputs share a graph the
// engine-native merge node is used; otherwise each pipeline runs concurrently
// and items are forwarded as they arrive.
//
// If any input pipeline returns an error the first error is propagated and all
// remaining pipelines are cancelled. Panics if no pipelines are provided.
func Merge[T any](ps ...*Pipeline[T]) *Pipeline[T] {
	if len(ps) == 0 {
		panic("kitsune: Merge requires at least one pipeline")
	}
	if len(ps) == 1 {
		return ps[0]
	}

	// Fast path: all same graph — use the engine-native Merge node.
	allSame := true
	for _, p := range ps[1:] {
		if p.g != ps[0].g {
			allSame = false
			break
		}
	}
	if allSame {
		g := ps[0].g
		inputs := make([]engine.InputRef, len(ps))
		for i, p := range ps {
			inputs[i] = engine.InputRef{Node: p.node, Port: p.port}
		}
		id := g.AddNode(&engine.Node{
			Kind:   engine.Merge,
			Inputs: inputs,
			Buffer: engine.DefaultBuffer,
		})
		return &Pipeline[T]{g: g, node: id}
	}

	// Independent graphs: run each pipeline concurrently and fan into one stream.
	return Generate(func(ctx context.Context, yield func(T) bool) error {
		ch := make(chan T, engine.DefaultBuffer)
		innerCtx, innerCancel := context.WithCancel(ctx)
		defer innerCancel()

		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		for _, p := range ps {
			p := p
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := p.ForEach(func(_ context.Context, item T) error {
					select {
					case ch <- item:
						return nil
					case <-innerCtx.Done():
						return innerCtx.Err()
					}
				}).Run(innerCtx)
				if err != nil && !errors.Is(err, context.Canceled) {
					errOnce.Do(func() { firstErr = err })
					innerCancel()
				}
			}()
		}

		// Close ch once all producers are done.
		go func() {
			wg.Wait()
			close(ch)
		}()

		for item := range ch {
			if !yield(item) {
				innerCancel()
				for range ch {
				}
				return nil
			}
		}

		return firstErr
	})
}

// ---------------------------------------------------------------------------
// Window
// ---------------------------------------------------------------------------

// Window collects items into slices based on time. Every duration d, the
// accumulated items are flushed as a batch. This is a convenience wrapper
// around [Batch] with an effectively unlimited size and a mandatory timeout.
// Pass [WithClock] to use a deterministic clock for testing.
func Window[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[[]T] {
	return Batch(p, math.MaxInt, append(opts, BatchTimeout(d))...)
}

// SlidingWindow collects items into overlapping windows of a fixed size,
// advancing by step items between successive windows.
// The first window is emitted once size items have been received; each
// subsequent window is emitted every step items.
//
// Panics if size <= 0, step <= 0, or step > size.
//
//	kitsune.SlidingWindow(p, 3, 1)  // [1,2,3], [2,3,4], [3,4,5]
//	kitsune.SlidingWindow(p, 4, 2)  // [1,2,3,4], [3,4,5,6]
//	kitsune.SlidingWindow(p, 3, 3)  // equivalent to Batch(3)
func SlidingWindow[T any](p *Pipeline[T], size, step int) *Pipeline[[]T] {
	if size <= 0 {
		panic("kitsune: SlidingWindow requires size > 0")
	}
	if step <= 0 {
		panic("kitsune: SlidingWindow requires step > 0")
	}
	if step > size {
		panic("kitsune: SlidingWindow requires step <= size")
	}
	// buf and sinceEmit are captured mutable state. Concurrency(1) is passed
	// explicitly to the inner FlatMap to enforce sequential execution, preventing
	// data races if this function is ever refactored to accept user opts.
	buf := make([]T, 0, size)
	sinceEmit := 0
	return FlatMap(p, func(_ context.Context, item T, yield func([]T) error) error {
		buf = append(buf, item)
		sinceEmit++
		if len(buf) < size {
			return nil // still filling the initial window
		}
		if sinceEmit < step {
			return nil // not yet time to slide
		}
		window := make([]T, size)
		copy(window, buf[len(buf)-size:])
		sinceEmit = 0
		// Retain only the overlap (size-step items) for the next window.
		keep := size - step
		if keep > 0 {
			retained := make([]T, keep)
			copy(retained, buf[len(buf)-keep:])
			buf = retained
		} else {
			buf = buf[:0]
		}
		return yield(window)
	}, Concurrency(1))
}

// ---------------------------------------------------------------------------
// Dedupe
// ---------------------------------------------------------------------------

// Dedupe drops items whose key has already been seen.
// Use [WithDedupSet] to supply a custom backend (e.g. Redis-backed) for
// cross-process deduplication; defaults to an in-process [MemoryDedupSet].
//
//	p.Dedupe(func(e Event) string { return e.ID })                                // in-process default
//	p.Dedupe(func(e Event) string { return e.ID }, kitsune.WithDedupSet(redisDedupSet)) // distributed
func (p *Pipeline[T]) Dedupe(key func(T) string, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	s := cfg.dedupSet
	if s == nil {
		s = MemoryDedupSet()
	}
	// Implemented as a Map so the pipeline context is available to the DedupSet
	// (e.g. for Redis-backed sets) and errors from Contains/Add halt the pipeline
	// instead of being silently dropped. engine.ErrSkipped is the sentinel that
	// causes the Map runner to discard the item without invoking the error handler.
	// Concurrency(1) is appended last to enforce sequential seen-key tracking.
	return Map(p, func(ctx context.Context, item T) (T, error) {
		k := key(item)
		seen, err := s.Contains(ctx, k)
		if err != nil {
			var zero T
			return zero, err
		}
		if seen {
			var zero T
			return zero, engine.ErrSkipped
		}
		if err := s.Add(ctx, k); err != nil {
			var zero T
			return zero, err
		}
		return item, nil
	}, append(opts, Concurrency(1))...)
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
func Scan[T, S any](p *Pipeline[T], initial S, fn func(S, T) S, opts ...StageOption) *Pipeline[S] {
	// state is captured mutable state. Concurrency(1) is appended last to
	// enforce sequential execution: each output depends on the previous accumulator,
	// so concurrent workers would both race on state and produce incorrect results.
	state := initial
	return Map(p, func(_ context.Context, item T) (S, error) {
		state = fn(state, item)
		return state, nil
	}, append(opts, Concurrency(1))...)
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

// Skip drops the first n items and emits all subsequent items.
// Skip(0) is a no-op; Skip with n ≥ stream length produces an empty pipeline.
//
//	// Discard the header row, then process data rows.
//	kitsune.DropWhile(lines, isHeader)          // predicate-based
//	lines.Skip(1)                               // count-based
//
// [Pipeline.Take] is the complement: it emits the first n items.
func (p *Pipeline[T]) Skip(n int) *Pipeline[T] {
	remaining := n
	return p.Filter(func(_ T) bool {
		if remaining > 0 {
			remaining--
			return false
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Reduce
// ---------------------------------------------------------------------------

// Reduce folds the entire stream into a single value using fn, emitting that
// value once the input closes. seed is the initial accumulator value.
//
//	// Sum all prices.
//	kitsune.Reduce(prices, 0.0, func(sum float64, p Price) float64 {
//	    return sum + p.Amount
//	})
//
//	// Collect into a custom struct.
//	kitsune.Reduce(events, Stats{}, func(s Stats, e Event) Stats {
//	    s.Count++
//	    return s
//	})
//
// On an empty stream, the seed is emitted unchanged — exactly one item is
// always produced. For streaming accumulators that emit after each item, use [Scan].
func Reduce[T, S any](p *Pipeline[T], seed S, fn func(S, T) S) *Pipeline[S] {
	fold := func(acc, item any) any { return fn(acc.(S), item.(T)) }
	id := p.g.AddNode(&engine.Node{
		Kind:         engine.ReduceNode,
		Fn:           fold,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:       engine.DefaultBuffer,
		ReduceSeed:   seed,
		ErrorHandler: engine.DefaultHandler{},
		Concurrency:  1,
	})
	return &Pipeline[S]{g: p.g, node: id}
}

// ---------------------------------------------------------------------------
// MapRecover
// ---------------------------------------------------------------------------

// MapRecover is like [Map] but calls recover whenever fn returns an error,
// substituting the returned value instead of halting or skipping the item.
// This lets you supply a default, log the failure, or transform the error
// into a sentinel value — all without touching the error-handling policy.
//
//	// Replace API errors with a zero-value result and log them.
//	kitsune.MapRecover(queries, callAPI,
//	    func(ctx context.Context, q Query, err error) Result {
//	        slog.WarnContext(ctx, "api error", "query", q, "err", err)
//	        return Result{}
//	    },
//	)
func MapRecover[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), recover func(context.Context, I, error) O, opts ...StageOption) *Pipeline[O] {
	wrapped := func(ctx context.Context, item I) (O, error) {
		result, err := fn(ctx, item)
		if err != nil {
			return recover(ctx, item, err), nil
		}
		return result, nil
	}
	return Map(p, wrapped, opts...)
}

// ---------------------------------------------------------------------------
// Throttle / Debounce
// ---------------------------------------------------------------------------

// Throttle emits at most one item per duration d. The first item that arrives
// after the cooldown window expires is forwarded; all items arriving within d
// of the last emission are dropped and reported to [OverflowHook] if present.
//
// Use Throttle for high-frequency event streams where only the first event in
// each window matters (e.g., rate-limiting API calls, coalescing UI events).
// For "emit last in quiet period" semantics, use [Debounce] instead.
// Pass [WithClock] to use a deterministic clock for testing.
//
//	// Allow at most one alert per 30 seconds.
//	kitsune.Throttle(alerts, 30*time.Second)
func Throttle[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	id := p.g.AddNode(&engine.Node{
		Kind:             engine.ThrottleNode,
		Name:             cfg.name,
		Inputs:           []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:           engine.DefaultBuffer,
		ThrottleDuration: d.Nanoseconds(),
		Clock:            cfg.clock,
	})
	return &Pipeline[T]{g: p.g, node: id}
}

// Debounce suppresses items while they keep arriving. Only the last item in a
// burst is emitted, after d has elapsed with no new items. Each new arrival
// resets the quiet-period timer and replaces the pending item.
//
// On input close, any pending item is flushed immediately.
//
// Use Debounce for scenarios where only the final item in a rapid series
// matters (e.g., search-as-you-type, configuration change coalescing).
// For "emit first in window" semantics, use [Throttle] instead.
// Pass [WithClock] to use a deterministic clock for testing.
//
//	// Emit only after 500ms of user inactivity.
//	kitsune.Debounce(keystrokes, 500*time.Millisecond)
func Debounce[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	id := p.g.AddNode(&engine.Node{
		Kind:             engine.DebounceNode,
		Name:             cfg.name,
		Inputs:           []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:           engine.DefaultBuffer,
		ThrottleDuration: d.Nanoseconds(),
		Clock:            cfg.clock,
	})
	return &Pipeline[T]{g: p.g, node: id}
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
// Zip pairs items from two pipelines by position into [Pair] values.
// The shorter stream determines the output length; the longer stream is
// cancelled once the shorter closes.
//
// Pipelines may come from the same graph or from completely independent graphs.
func Zip[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	if a.g == b.g {
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

	// Independent graphs: run both concurrently and pair by position.
	return Generate(func(ctx context.Context, yield func(Pair[A, B]) bool) error {
		chA := make(chan A, engine.DefaultBuffer)
		chB := make(chan B, engine.DefaultBuffer)
		innerCtx, innerCancel := context.WithCancel(ctx)

		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		wg.Add(2)
		go func() {
			defer wg.Done()
			err := a.ForEach(func(_ context.Context, item A) error {
				select {
				case chA <- item:
					return nil
				case <-innerCtx.Done():
					return innerCtx.Err()
				}
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
			close(chA)
		}()
		go func() {
			defer wg.Done()
			err := b.ForEach(func(_ context.Context, item B) error {
				select {
				case chB <- item:
					return nil
				case <-innerCtx.Done():
					return innerCtx.Err()
				}
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
			close(chB)
		}()

		// Pair items by position; stop when either side closes or yield returns false.
		for {
			va, okA := <-chA
			if !okA {
				innerCancel()
				for range chB {
				}
				break
			}
			vb, okB := <-chB
			if !okB {
				innerCancel()
				for range chA {
				}
				break
			}
			if !yield(Pair[A, B]{First: va, Second: vb}) {
				innerCancel()
				for range chA {
				}
				for range chB {
				}
				break
			}
		}

		innerCancel()
		wg.Wait()
		return firstErr
	})
}

// ZipWith pairs items from two pipelines by position and combines each pair
// immediately using fn, avoiding the intermediate [Pair] value.
// It is equivalent to [Map] applied to [Zip] but expressed in a single call.
//
// Pipelines may come from the same graph or from completely independent graphs.
//
//	// Multiply paired values from two branches.
//	kitsune.ZipWith(as, bs, func(_ context.Context, a, b int) (int, error) {
//	    return a * b, nil
//	})
func ZipWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	return Map(Zip(a, b), func(ctx context.Context, p Pair[A, B]) (O, error) {
		return fn(ctx, p.First, p.Second)
	}, opts...)
}

// Pairwise emits consecutive overlapping pairs from the stream.
// A stream of N items produces N-1 pairs; a stream with fewer than 2 items
// produces nothing.
//
//	kitsune.Pairwise(kitsune.FromSlice([]int{1, 2, 3, 4}))
//	// → Pair{1,2}, Pair{2,3}, Pair{3,4}
func Pairwise[T any](p *Pipeline[T]) *Pipeline[Pair[T, T]] {
	var prev T
	hasPrev := false
	return FlatMap(p, func(_ context.Context, item T, yield func(Pair[T, T]) error) error {
		if !hasPrev {
			prev = item
			hasPrev = true
			return nil
		}
		pair := Pair[T, T]{First: prev, Second: item}
		prev = item
		return yield(pair)
	})
}

// ---------------------------------------------------------------------------
// Error routing
// ---------------------------------------------------------------------------

// ErrItem holds an input item alongside the error returned by its processing
// function. Produced by [MapResult] on the failed output pipeline.
type ErrItem[I any] struct {
	Item I
	Err  error
}

// MapResult applies a 1:1 transformation and routes results by outcome:
// successful outputs go to the first (ok) pipeline; failures go to the second
// (failed) pipeline as [ErrItem] values containing the original input and error.
//
//	ok, failed := kitsune.MapResult(p, fetchUser)
//	// ok:     *Pipeline[User]       — successful lookups
//	// failed: *Pipeline[ErrItem[ID]] — items that errored, with original input
//
// Unlike [Map] with [OnError], MapResult never halts, skips, or retries —
// every error is always routed to the failed pipeline.
// Both output pipelines must be consumed (same rule as [Partition]).
func MapResult[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) (*Pipeline[O], *Pipeline[ErrItem[I]]) {
	wrapped := func(ctx context.Context, in any) (any, error) {
		return fn(ctx, in.(I))
	}
	wrapErr := func(in any, err error) any {
		return ErrItem[I]{Item: in.(I), Err: err}
	}
	cfg := buildStageConfig(opts)
	n := &engine.Node{
		Kind:             engine.MapResultNode,
		Name:             cfg.name,
		Fn:               wrapped,
		Inputs:           []engine.InputRef{{Node: p.node, Port: p.port}},
		Buffer:           cfg.buffer,
		MapResultErrWrap: wrapErr,
	}
	id := p.g.AddNode(n)
	return &Pipeline[O]{g: p.g, node: id, port: 0},
		&Pipeline[ErrItem[I]]{g: p.g, node: id, port: 1}
}

// ---------------------------------------------------------------------------
// DeadLetter / DeadLetterSink
// ---------------------------------------------------------------------------

// DeadLetter applies a transformation with optional retry and routes results
// by outcome: successful outputs go to the first (ok) pipeline; items that
// fail permanently (after retries are exhausted) go to the second (dead-letter)
// pipeline as [ErrItem] values containing the original input and final error.
//
// Use [OnError] with [Retry] in opts to retry transient failures before routing
// to the dead-letter pipeline:
//
//	ok, dlq := kitsune.DeadLetter(p, fetchUser,
//	    kitsune.OnError(kitsune.Retry(3, kitsune.ExponentialBackoff(10*time.Millisecond, time.Second))),
//	)
//	// ok:  *Pipeline[User]        — successful lookups
//	// dlq: *Pipeline[ErrItem[ID]] — permanently failed items
//
// Both output pipelines must be consumed (same rule as [Partition]).
func DeadLetter[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) (*Pipeline[O], *Pipeline[ErrItem[I]]) {
	cfg := buildStageConfig(opts)
	handler := cfg.errorHandler

	// retrying wraps fn with the retry loop from opts. On permanent failure the
	// error propagates to MapResult, which routes it to the dead-letter port.
	retrying := func(ctx context.Context, item I) (O, error) {
		adapted := func(ctx context.Context, in any) (any, error) {
			out, err := fn(ctx, in.(I))
			return out, err
		}
		result, err, _ := engine.ProcessItem(ctx, adapted, item, handler)
		if err != nil {
			var zero O
			return zero, err
		}
		return result.(O), nil
	}

	// MapResult does not use an error handler — all errors route to port 1.
	// Strip OnError from the opts forwarded to MapResult so it has no effect on
	// the node configuration; the retry was already applied inside retrying.
	return MapResult(p, retrying, deadLetterPassOpts(opts)...)
}

// DeadLetterSink attaches a terminal sink with optional retry and routes
// permanently-failed items to the returned dead-letter pipeline.
// The second return value is a [Runner] that drives the whole graph.
//
//	dlq, runner := kitsune.DeadLetterSink(p, writeToDB,
//	    kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(50*time.Millisecond))),
//	)
//	// dlq:    *Pipeline[ErrItem[T]] — failed items for inspection / re-queuing
//	// runner: *Runner — drive execution with runner.Run(ctx)
//
// Both dlq and runner share the same graph; dlq must be consumed before calling
// runner.Run (e.g. via dlq.ForEach or dlq.Collect within the same Run call).
func DeadLetterSink[I any](p *Pipeline[I], fn func(context.Context, I) error, opts ...StageOption) (*Pipeline[ErrItem[I]], *Runner) {
	cfg := buildStageConfig(opts)
	handler := cfg.errorHandler

	adapted := func(ctx context.Context, item I) (struct{}, error) {
		inner := func(ctx context.Context, in any) (any, error) {
			return struct{}{}, fn(ctx, in.(I))
		}
		_, err, _ := engine.ProcessItem(ctx, inner, item, handler)
		return struct{}{}, err
	}

	ok, dlq := MapResult(p, adapted, deadLetterPassOpts(opts)...)
	return dlq, ok.Drain()
}

// deadLetterPassOpts filters out OnError options because [MapResult] routes all
// errors to its dead-letter port; passing an error handler there would be ignored
// and misleading.
func deadLetterPassOpts(opts []StageOption) []StageOption {
	pass := make([]StageOption, 0, len(opts))
	for _, o := range opts {
		var c stageConfig
		o(&c)
		if c.errorHandler != nil {
			continue
		}
		pass = append(pass, o)
	}
	return pass
}

// ---------------------------------------------------------------------------
// WithLatestFrom
// ---------------------------------------------------------------------------

// WithLatestFrom combines each item from the primary pipeline with the most
// recent value seen from the secondary pipeline, emitting a [Pair].
//
// The secondary pipeline updates a shared "latest" value on every item it
// produces, but does not drive output — only primary items cause emissions.
// Primary items that arrive before any secondary value has been seen are
// silently dropped.
//
// Both pipelines must share the same graph. Panics otherwise.
//
//	// Enrich each click event with the current mouse position:
//	kitsune.WithLatestFrom(clicks, positions)
//	// → Pair{click, latestPosition} for each click after the first position
// WithLatestFrom combines each primary item with the most recently seen value
// from secondary. Primary items that arrive before secondary has emitted any
// value are silently dropped.
//
// Pipelines may come from the same graph or from completely independent graphs.
func WithLatestFrom[A, B any](primary *Pipeline[A], secondary *Pipeline[B]) *Pipeline[Pair[A, B]] {
	if primary.g == secondary.g {
		convert := func(va, vb any) any {
			return Pair[A, B]{First: va.(A), Second: vb.(B)}
		}
		id := primary.g.AddNode(&engine.Node{
			Kind:       engine.WithLatestFromNode,
			Inputs:     []engine.InputRef{{Node: primary.node, Port: primary.port}, {Node: secondary.node, Port: secondary.port}},
			Buffer:     engine.DefaultBuffer,
			ZipConvert: convert,
		})
		return &Pipeline[Pair[A, B]]{g: primary.g, node: id}
	}

	// Independent graphs: drain secondary into a mutex-protected latest value
	// while forwarding primary items through a channel.
	return Generate(func(ctx context.Context, yield func(Pair[A, B]) bool) error {
		primCh := make(chan A, engine.DefaultBuffer)
		innerCtx, innerCancel := context.WithCancel(ctx)

		var (
			mu       sync.Mutex
			latest   B
			hasValue bool
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		// Background: continuously update latest from secondary.
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := secondary.ForEach(func(_ context.Context, item B) error {
				mu.Lock()
				latest = item
				hasValue = true
				mu.Unlock()
				return nil
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
		}()

		// Background: forward primary items through primCh.
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := primary.ForEach(func(_ context.Context, item A) error {
				select {
				case primCh <- item:
					return nil
				case <-innerCtx.Done():
					return innerCtx.Err()
				}
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
			close(primCh)
		}()

		for item := range primCh {
			mu.Lock()
			hv, cur := hasValue, latest
			mu.Unlock()
			if !hv {
				continue // drop until secondary has emitted
			}
			if !yield(Pair[A, B]{First: item, Second: cur}) {
				innerCancel()
				for range primCh {
				}
				break
			}
		}

		innerCancel()
		wg.Wait()
		return firstErr
	})
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

// CombineLatest combines two pipelines such that whenever either side emits,
// the output receives a Pair of the latest value from each side.
// No output is produced until both sides have emitted at least once.
//
// CombineLatest is the symmetric counterpart to [WithLatestFrom] — either side
// triggers output. Use it for price feeds, sensor fusion, or any flow where
// both streams are equally authoritative.
//
// Pipelines may come from the same graph or from completely independent graphs.
func CombineLatest[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]] {
	if a.g == b.g {
		convert := func(va, vb any) any {
			return Pair[A, B]{First: va.(A), Second: vb.(B)}
		}
		id := a.g.AddNode(&engine.Node{
			Kind:       engine.CombineLatestNode,
			Inputs:     []engine.InputRef{{Node: a.node, Port: a.port}, {Node: b.node, Port: b.port}},
			Buffer:     engine.DefaultBuffer,
			ZipConvert: convert,
		})
		return &Pipeline[Pair[A, B]]{g: a.g, node: id}
	}

	// Independent graphs: run both pipelines concurrently, maintain latest values,
	// and emit a pair whenever either side produces a new value.
	return Generate(func(ctx context.Context, yield func(Pair[A, B]) bool) error {
		chA := make(chan A, 1)
		chB := make(chan B, 1)
		innerCtx, innerCancel := context.WithCancel(ctx)

		var (
			mu       sync.Mutex
			latestA  A
			latestB  B
			hasA     bool
			hasB     bool
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)

		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.ForEach(func(_ context.Context, item A) error {
				select {
				case chA <- item:
					return nil
				case <-innerCtx.Done():
					return innerCtx.Err()
				}
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
			close(chA)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.ForEach(func(_ context.Context, item B) error {
				select {
				case chB <- item:
					return nil
				case <-innerCtx.Done():
					return innerCtx.Err()
				}
			}).Run(innerCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				errOnce.Do(func() { firstErr = err })
				innerCancel()
			}
			close(chB)
		}()

		doneA, doneB := false, false
		for !doneA || !doneB {
			select {
			case item, ok := <-chA:
				if !ok {
					doneA = true
					chA = nil
					continue
				}
				mu.Lock()
				latestA = item
				hasA = true
				hb, curB := hasB, latestB
				mu.Unlock()
				if !hb {
					continue
				}
				if !yield(Pair[A, B]{First: item, Second: curB}) {
					innerCancel()
					for range chA {
					}
					for range chB {
					}
					wg.Wait()
					return firstErr
				}
			case item, ok := <-chB:
				if !ok {
					doneB = true
					chB = nil
					continue
				}
				mu.Lock()
				latestB = item
				hasB = true
				ha, curA := hasA, latestA
				mu.Unlock()
				if !ha {
					continue
				}
				if !yield(Pair[A, B]{First: curA, Second: item}) {
					innerCancel()
					for range chA {
					}
					for range chB {
					}
					wg.Wait()
					return firstErr
				}
			case <-innerCtx.Done():
				wg.Wait()
				return firstErr
			}
		}

		innerCancel()
		wg.Wait()
		return firstErr
	})
}

// CombineLatestWith is like [CombineLatest] but applies fn to each emitted pair.
func CombineLatestWith[A, B, O any](a *Pipeline[A], b *Pipeline[B], fn func(context.Context, A, B) (O, error), opts ...StageOption) *Pipeline[O] {
	return Map(CombineLatest(a, b), func(ctx context.Context, p Pair[A, B]) (O, error) {
		return fn(ctx, p.First, p.Second)
	}, opts...)
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

// Balance distributes items from p across n output pipelines in round-robin order.
// Each item goes to exactly one output; use [Broadcast] to copy to all outputs.
//
// Balance completes the fan-out vocabulary alongside [Broadcast] (copy to all)
// and [Partition] (split by predicate).
//
// Balance panics if n <= 0.
func Balance[T any](p *Pipeline[T], n int) []*Pipeline[T] {
	if n <= 0 {
		panic("kitsune: Balance requires n > 0")
	}
	id := p.g.AddNode(&engine.Node{
		Kind:       engine.BalanceNode,
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

// LiftPure wraps a context-free, error-free function for use with [Map] and [FlatMap].
// Unlike [Lift], the wrapped function never fails.
//
//	doubled := kitsune.Map(numbers, kitsune.LiftPure(func(n int) int { return n * 2 }))
func LiftPure[I, O any](fn func(I) O) func(context.Context, I) (O, error) {
	return func(_ context.Context, in I) (O, error) {
		return fn(in), nil
	}
}

// ---------------------------------------------------------------------------
// Reject
// ---------------------------------------------------------------------------

// Reject keeps only items for which pred returns false. It is the inverse of [Pipeline.Filter].
//
//	kitsune.Reject(p, func(n int) bool { return n%2 == 0 }) // keep odd numbers
func Reject[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T] {
	return p.Filter(func(item T) bool { return !pred(item) })
}

// ---------------------------------------------------------------------------
// WithIndex
// ---------------------------------------------------------------------------

// WithIndex pairs each item with its 0-based stream position.
//
//	kitsune.WithIndex(kitsune.FromSlice([]string{"a","b","c"}))
//	// → Pair{0,"a"}, Pair{1,"b"}, Pair{2,"c"}
func WithIndex[T any](p *Pipeline[T]) *Pipeline[Pair[int, T]] {
	i := -1
	return Map(p, func(_ context.Context, item T) (Pair[int, T], error) {
		i++
		return Pair[int, T]{First: i, Second: item}, nil
	})
}

// ---------------------------------------------------------------------------
// Intersperse / MapIntersperse
// ---------------------------------------------------------------------------

// Intersperse inserts sep between consecutive items in the stream.
// The separator is inserted only between items, not before the first or after the last.
//
//	kitsune.Intersperse(kitsune.FromSlice([]string{"a","b","c"}), ",")
//	// → "a", ",", "b", ",", "c"
func Intersperse[T any](p *Pipeline[T], sep T) *Pipeline[T] {
	first := true
	return FlatMap(p, func(_ context.Context, item T, yield func(T) error) error {
		if first {
			first = false
			return yield(item)
		}
		if err := yield(sep); err != nil {
			return err
		}
		return yield(item)
	})
}

// MapIntersperse applies fn to each item and inserts sep between mapped values.
// Equivalent to [Intersperse] applied after [Map], but in a single pass.
//
//	kitsune.MapIntersperse(kitsune.FromSlice([]int{1,2,3}), 0,
//	    func(_ context.Context, n int) (int, error) { return n * 10, nil },
//	)
//	// → 10, 0, 20, 0, 30
func MapIntersperse[T, O any](p *Pipeline[T], sep O, fn func(context.Context, T) (O, error)) *Pipeline[O] {
	first := true
	return FlatMap(p, func(ctx context.Context, item T, yield func(O) error) error {
		out, err := fn(ctx, item)
		if err != nil {
			return err
		}
		if first {
			first = false
			return yield(out)
		}
		if err := yield(sep); err != nil {
			return err
		}
		return yield(out)
	})
}

// ---------------------------------------------------------------------------
// TakeEvery / DropEvery / MapEvery
// ---------------------------------------------------------------------------

// TakeEvery emits every nth item starting from index 0.
// TakeEvery(p, 1) is a pass-through; TakeEvery(p, 2) emits items at indices 0, 2, 4, …
// Panics if nth <= 0.
//
//	kitsune.TakeEvery(kitsune.FromSlice([]int{1,2,3,4,5,6}), 2)
//	// → 1, 3, 5
func TakeEvery[T any](p *Pipeline[T], nth int) *Pipeline[T] {
	if nth <= 0 {
		panic("kitsune: TakeEvery requires nth > 0")
	}
	i := 0
	return p.Filter(func(_ T) bool {
		emit := i%nth == 0
		i++
		return emit
	})
}

// DropEvery drops every nth item (0-indexed) and emits all others.
// DropEvery(p, 3) drops items at indices 0, 3, 6, …
// Panics if nth <= 0.
//
//	kitsune.DropEvery(kitsune.FromSlice([]int{1,2,3,4,5,6}), 2)
//	// → 2, 4, 6
func DropEvery[T any](p *Pipeline[T], nth int) *Pipeline[T] {
	if nth <= 0 {
		panic("kitsune: DropEvery requires nth > 0")
	}
	i := 0
	return p.Filter(func(_ T) bool {
		drop := i%nth == 0
		i++
		return !drop
	})
}

// MapEvery applies fn to every nth item (0-indexed) and passes all other items
// through unchanged. Panics if nth <= 0.
//
//	// Double every third item.
//	kitsune.MapEvery(p, 3, func(_ context.Context, n int) (int, error) { return n * 2, nil })
func MapEvery[T any](p *Pipeline[T], nth int, fn func(context.Context, T) (T, error)) *Pipeline[T] {
	if nth <= 0 {
		panic("kitsune: MapEvery requires nth > 0")
	}
	i := 0
	return Map(p, func(ctx context.Context, item T) (T, error) {
		apply := i%nth == 0
		i++
		if apply {
			return fn(ctx, item)
		}
		return item, nil
	})
}

// ---------------------------------------------------------------------------
// ConsecutiveDedup / ConsecutiveDedupBy
// ---------------------------------------------------------------------------

// ConsecutiveDedup drops consecutive duplicate items, keeping only the first
// in each run of equal values.
// Unlike [Distinct], it does not deduplicate across the whole stream — the same
// value may appear multiple times as long as it is not adjacent to itself.
// T must be comparable.
//
//	kitsune.ConsecutiveDedup(kitsune.FromSlice([]int{1,1,2,3,3,3,2}))
//	// → 1, 2, 3, 2
func ConsecutiveDedup[T comparable](p *Pipeline[T]) *Pipeline[T] {
	// prev and hasPrev are captured mutable state. Safety is guaranteed by the
	// engine: Filter nodes are always dispatched through a single goroutine
	// (runFilter has no concurrency path), so no synchronization is needed.
	var prev T
	hasPrev := false
	return p.Filter(func(item T) bool {
		if hasPrev && item == prev {
			return false
		}
		prev = item
		hasPrev = true
		return true
	})
}

// ConsecutiveDedupBy drops consecutive items that produce the same key under fn.
// Use this when T is not comparable or when deduplication should be based on a
// derived field.
//
//	kitsune.ConsecutiveDedupBy(events, func(e Event) string { return e.Type })
func ConsecutiveDedupBy[T any, K comparable](p *Pipeline[T], fn func(T) K) *Pipeline[T] {
	// prevKey and hasPrev are captured mutable state. Safety is guaranteed by the
	// engine: Filter nodes are always single-goroutine — see ConsecutiveDedup.
	var prevKey K
	hasPrev := false
	return p.Filter(func(item T) bool {
		k := fn(item)
		if hasPrev && k == prevKey {
			return false
		}
		prevKey = k
		hasPrev = true
		return true
	})
}
