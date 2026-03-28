package kitsune

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// Numeric is satisfied by all integer and floating-point numeric types.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// ForEach creates a terminal stage that processes each item.
// Returns a [Runner] for deferred execution.
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *Runner {
	wrapped := func(ctx context.Context, in any) error {
		return fn(ctx, in.(T))
	}
	scfg := buildStageConfig(opts)
	p.g.AddNode(&engine.Node{
		Kind:         engine.Sink,
		Name:         scfg.name,
		Fn:           wrapped,
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Concurrency:  scfg.concurrency,
		Ordered:      scfg.ordered,
		Buffer:       scfg.buffer,
		Overflow:     scfg.overflow,
		ErrorHandler: scfg.errorHandler,
		Supervision:  scfg.supervision,
	})
	return &Runner{g: p.g}
}

// Drain creates a terminal that consumes and discards all items.
// Useful when side effects happen in [Pipeline.Tap] or [Map] stages.
func (p *Pipeline[T]) Drain() *Runner {
	p.g.AddNode(&engine.Node{
		Kind:         engine.Sink,
		Fn:           func(context.Context, any) error { return nil },
		Inputs:       []engine.InputRef{{Node: p.node, Port: p.port}},
		Concurrency:  1,
		Buffer:       engine.DefaultBuffer,
		ErrorHandler: engine.DefaultHandler{},
	})
	return &Runner{g: p.g}
}

// Collect runs the pipeline and materializes all output items into a slice.
func (p *Pipeline[T]) Collect(ctx context.Context, opts ...RunOption) ([]T, error) {
	var results []T
	runner := p.ForEach(func(_ context.Context, item T) error {
		results = append(results, item)
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return results, nil
}

// First runs the pipeline and returns the first item emitted.
// The second return value is false if the pipeline produced no items.
// The pipeline is stopped early as soon as the first item is received.
func (p *Pipeline[T]) First(ctx context.Context, opts ...RunOption) (T, bool, error) {
	results, err := p.Take(1).Collect(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	if len(results) == 0 {
		var zero T
		return zero, false, nil
	}
	return results[0], true, nil
}

// Last runs the pipeline and returns the final item emitted.
// The second return value is false if the pipeline produced no items.
func (p *Pipeline[T]) Last(ctx context.Context, opts ...RunOption) (T, bool, error) {
	results, err := p.Collect(ctx, opts...)
	if err != nil {
		var zero T
		return zero, false, err
	}
	if len(results) == 0 {
		var zero T
		return zero, false, nil
	}
	return results[len(results)-1], true, nil
}

// Count runs the pipeline and returns the total number of items emitted.
func (p *Pipeline[T]) Count(ctx context.Context, opts ...RunOption) (int64, error) {
	var n atomic.Int64
	runner := p.ForEach(func(_ context.Context, _ T) error {
		n.Add(1)
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return 0, err
	}
	return n.Load(), nil
}

// Any runs the pipeline and returns true if at least one item satisfies fn.
// The pipeline is stopped early as soon as a matching item is found.
func (p *Pipeline[T]) Any(ctx context.Context, fn func(T) bool, opts ...RunOption) (bool, error) {
	results, err := p.Filter(fn).Take(1).Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

// All runs the pipeline and returns true if every item satisfies fn.
// The pipeline is stopped early as soon as a non-matching item is found.
func (p *Pipeline[T]) All(ctx context.Context, fn func(T) bool, opts ...RunOption) (bool, error) {
	results, err := p.Filter(func(item T) bool { return !fn(item) }).Take(1).Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	return len(results) == 0, nil
}

// ---------------------------------------------------------------------------
// Sum
// ---------------------------------------------------------------------------

// Sum runs the pipeline and returns the sum of all items.
// Returns the zero value if the pipeline is empty.
func Sum[T Numeric](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error) {
	results, err := Reduce(p, *new(T), func(acc, item T) T { return acc + item }).Collect(ctx, opts...)
	if err != nil || len(results) == 0 {
		return *new(T), err
	}
	return results[0], nil
}

// ---------------------------------------------------------------------------
// Min / Max / MinMax
// ---------------------------------------------------------------------------

// Min runs the pipeline and returns the smallest item according to less.
// The second return value is false if the pipeline produced no items.
//
//	v, ok, err := kitsune.Min(ctx, p, func(a, b int) bool { return a < b })
func Min[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil || len(items) == 0 {
		var zero T
		return zero, false, err
	}
	m := items[0]
	for _, item := range items[1:] {
		if less(item, m) {
			m = item
		}
	}
	return m, true, nil
}

// Max runs the pipeline and returns the largest item according to less.
// The second return value is false if the pipeline produced no items.
//
//	v, ok, err := kitsune.Max(ctx, p, func(a, b int) bool { return a < b })
func Max[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil || len(items) == 0 {
		var zero T
		return zero, false, err
	}
	m := items[0]
	for _, item := range items[1:] {
		if less(m, item) {
			m = item
		}
	}
	return m, true, nil
}

// MinMax runs the pipeline and returns both the smallest and largest items in a
// single pass. The second return value is false if the pipeline produced no items.
// The returned Pair carries (min, max).
//
//	pair, ok, err := kitsune.MinMax(ctx, p, func(a, b int) bool { return a < b })
//	// pair.First == min, pair.Second == max
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (Pair[T, T], bool, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil || len(items) == 0 {
		return Pair[T, T]{}, false, err
	}
	mn, mx := items[0], items[0]
	for _, item := range items[1:] {
		if less(item, mn) {
			mn = item
		}
		if less(mx, item) {
			mx = item
		}
	}
	return Pair[T, T]{First: mn, Second: mx}, true, nil
}

// ---------------------------------------------------------------------------
// MinBy / MaxBy
// ---------------------------------------------------------------------------

// MinBy runs the pipeline and returns the item whose key is smallest.
// The second return value is false if the pipeline produced no items.
//
//	kitsune.MinBy(ctx, users, func(u User) int { return u.Age },
//	    func(a, b int) bool { return a < b },
//	)
func MinBy[T any, K any](ctx context.Context, p *Pipeline[T], key func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil || len(items) == 0 {
		var zero T
		return zero, false, err
	}
	m := items[0]
	mk := key(m)
	for _, item := range items[1:] {
		k := key(item)
		if less(k, mk) {
			m = item
			mk = k
		}
	}
	return m, true, nil
}

// MaxBy runs the pipeline and returns the item whose key is largest.
// The second return value is false if the pipeline produced no items.
//
//	kitsune.MaxBy(ctx, users, func(u User) int { return u.Age },
//	    func(a, b int) bool { return a < b },
//	)
func MaxBy[T any, K any](ctx context.Context, p *Pipeline[T], key func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil || len(items) == 0 {
		var zero T
		return zero, false, err
	}
	m := items[0]
	mk := key(m)
	for _, item := range items[1:] {
		k := key(item)
		if less(mk, k) {
			m = item
			mk = k
		}
	}
	return m, true, nil
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

// Find runs the pipeline and returns the first item that satisfies pred.
// The second return value is false if no item matched.
// The pipeline is stopped as soon as a match is found.
//
//	user, ok, err := kitsune.Find(ctx, users, func(u User) bool { return u.Admin })
func Find[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (T, bool, error) {
	return p.Filter(pred).First(ctx, opts...)
}

// ---------------------------------------------------------------------------
// Frequencies / FrequenciesBy
// ---------------------------------------------------------------------------

// Frequencies runs the pipeline and returns a map of each distinct item to the
// number of times it appeared. T must be comparable.
//
//	kitsune.Frequencies(ctx, kitsune.FromSlice([]string{"a","b","a","c","b","a"}))
//	// → map["a":3 "b":2 "c":1]
func Frequencies[T comparable](ctx context.Context, p *Pipeline[T], opts ...RunOption) (map[T]int, error) {
	m := make(map[T]int)
	runner := p.ForEach(func(_ context.Context, item T) error {
		m[item]++
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return m, nil
}

// FrequenciesBy runs the pipeline and returns a map of each distinct key to the
// number of items that mapped to it.
//
//	kitsune.FrequenciesBy(ctx, words, func(w string) int { return len(w) })
//	// count words by length
func FrequenciesBy[T any, K comparable](ctx context.Context, p *Pipeline[T], key func(T) K, opts ...RunOption) (map[K]int, error) {
	m := make(map[K]int)
	runner := p.ForEach(func(_ context.Context, item T) error {
		m[key(item)]++
		return nil
	})
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// ReduceWhile
// ---------------------------------------------------------------------------

// errReduceWhileDone is a sentinel returned by the ForEach fn to signal early
// termination. It is intercepted before being returned to the caller.
var errReduceWhileDone = errors.New("kitsune: reduce-while done")

// ReduceWhile folds the stream like [Reduce], but fn may signal early
// termination by returning false as its second value. The accumulator is
// updated even for the item that triggers the halt.
// seed is the initial accumulator value.
//
//	// Sum until the running total exceeds 10.
//	kitsune.ReduceWhile(ctx, p, 0, func(acc, n int) (int, bool) {
//	    acc += n
//	    return acc, acc <= 10
//	})
func ReduceWhile[T, S any](ctx context.Context, p *Pipeline[T], seed S, fn func(S, T) (S, bool), opts ...RunOption) (S, error) {
	acc := seed
	runner := p.ForEach(func(_ context.Context, item T) error {
		next, cont := fn(acc, item)
		acc = next
		if !cont {
			return errReduceWhileDone
		}
		return nil
	})
	err := runner.Run(ctx, opts...)
	if errors.Is(err, errReduceWhileDone) {
		return acc, nil
	}
	return acc, err
}

// ---------------------------------------------------------------------------
// TakeRandom
// ---------------------------------------------------------------------------

// TakeRandom runs the pipeline and returns n items chosen uniformly at random,
// in a random order. If the pipeline produces fewer than n items, all items are
// returned in a random order.
//
//	kitsune.TakeRandom(ctx, kitsune.FromSlice([]int{1,2,3,4,5}), 3)
//	// → some 3-element subset, in random order
func TakeRandom[T any](ctx context.Context, p *Pipeline[T], n int, opts ...RunOption) ([]T, error) {
	items, err := p.Collect(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if n >= len(items) {
		result := make([]T, len(items))
		copy(result, items)
		rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
		return result, nil
	}
	// Reservoir sampling (Algorithm R).
	result := make([]T, n)
	copy(result, items[:n])
	for i := n; i < len(items); i++ {
		j := rand.IntN(i + 1)
		if j < n {
			result[j] = items[i]
		}
	}
	rand.Shuffle(n, func(i, j int) { result[i], result[j] = result[j], result[i] })
	return result, nil
}
