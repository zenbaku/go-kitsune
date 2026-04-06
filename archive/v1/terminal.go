package kitsune

import (
	"context"
	"errors"
	"iter"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/zenbaku/go-kitsune/engine"
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
	}, Concurrency(1))
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return results, nil
}

// Iter returns a pull-based iterator over the pipeline's output items and an
// error function. The iterator is suitable for use with range-over-func
// (Go 1.23+).
//
// The error function must be called after iteration completes — or after
// breaking out of the loop — to retrieve any pipeline execution error. It
// blocks until the pipeline finishes and is safe to call multiple times.
//
// If the caller breaks out of the loop early, the pipeline context is
// cancelled and the error function returns nil (the context.Canceled caused
// by the break is suppressed). If the caller's own context is cancelled, the
// error function returns context.Canceled.
//
//	seq, errFn := p.Iter(ctx)
//	for item := range seq {
//	    process(item)
//	}
//	if err := errFn(); err != nil {
//	    log.Fatal(err)
//	}
//
// If the iterator is not fully consumed, the caller must cancel the context
// to release pipeline resources.
func (p *Pipeline[T]) Iter(ctx context.Context, opts ...RunOption) (iter.Seq[T], func() error) {
	ch := make(chan T, engine.DefaultBuffer)
	iterCtx, iterCancel := context.WithCancel(ctx)
	var callerBroke atomic.Bool

	runner := p.ForEach(func(_ context.Context, item T) error {
		select {
		case ch <- item:
			return nil
		case <-iterCtx.Done():
			return iterCtx.Err()
		}
	}, Concurrency(1))

	handle := runner.RunAsync(iterCtx, opts...)

	go func() {
		<-handle.Done()
		close(ch)
	}()

	seq := func(yield func(T) bool) {
		defer func() {
			iterCancel()
			for range ch { //nolint:revive
			}
		}()
		for item := range ch {
			if !yield(item) {
				callerBroke.Store(true)
				return
			}
		}
	}

	var (
		errOnce sync.Once
		errVal  error
	)
	errFn := func() error {
		errOnce.Do(func() {
			errVal = <-handle.Err()
			if callerBroke.Load() && errors.Is(errVal, context.Canceled) {
				errVal = nil
			}
		})
		return errVal
	}

	return seq, errFn
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

// optional holds a value that may or may not be present.
type optional[T any] struct {
	value T
	valid bool
}

// byAcc is the accumulator for MinBy/MaxBy: tracks both the winning item and
// its extracted key so the key function is called exactly once per item.
type byAcc[T, K any] struct {
	item  T
	key   K
	valid bool
}

// Min runs the pipeline and returns the smallest item according to less.
// The second return value is false if the pipeline produced no items.
//
//	v, ok, err := kitsune.Min(ctx, p, func(a, b int) bool { return a < b })
func Min[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	results, err := Reduce(p, optional[T]{}, func(acc optional[T], item T) optional[T] {
		if !acc.valid || less(item, acc.value) {
			return optional[T]{value: item, valid: true}
		}
		return acc
	}).Collect(ctx, opts...)
	if err != nil || len(results) == 0 || !results[0].valid {
		var zero T
		return zero, false, err
	}
	return results[0].value, true, nil
}

// Max runs the pipeline and returns the largest item according to less.
// The second return value is false if the pipeline produced no items.
//
//	v, ok, err := kitsune.Max(ctx, p, func(a, b int) bool { return a < b })
func Max[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error) {
	results, err := Reduce(p, optional[T]{}, func(acc optional[T], item T) optional[T] {
		if !acc.valid || less(acc.value, item) {
			return optional[T]{value: item, valid: true}
		}
		return acc
	}).Collect(ctx, opts...)
	if err != nil || len(results) == 0 || !results[0].valid {
		var zero T
		return zero, false, err
	}
	return results[0].value, true, nil
}

// MinMax runs the pipeline and returns both the smallest and largest items in a
// single pass. The second return value is false if the pipeline produced no items.
// The returned Pair carries (min, max).
//
//	pair, ok, err := kitsune.MinMax(ctx, p, func(a, b int) bool { return a < b })
//	// pair.First == min, pair.Second == max
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (Pair[T, T], bool, error) {
	type minMaxAcc struct {
		mn, mx optional[T]
	}
	results, err := Reduce(p, minMaxAcc{}, func(acc minMaxAcc, item T) minMaxAcc {
		if !acc.mn.valid || less(item, acc.mn.value) {
			acc.mn = optional[T]{value: item, valid: true}
		}
		if !acc.mx.valid || less(acc.mx.value, item) {
			acc.mx = optional[T]{value: item, valid: true}
		}
		return acc
	}).Collect(ctx, opts...)
	if err != nil || len(results) == 0 || !results[0].mn.valid {
		return Pair[T, T]{}, false, err
	}
	r := results[0]
	return Pair[T, T]{First: r.mn.value, Second: r.mx.value}, true, nil
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
	results, err := Reduce(p, byAcc[T, K]{}, func(acc byAcc[T, K], item T) byAcc[T, K] {
		k := key(item)
		if !acc.valid || less(k, acc.key) {
			return byAcc[T, K]{item: item, key: k, valid: true}
		}
		return acc
	}).Collect(ctx, opts...)
	if err != nil || len(results) == 0 || !results[0].valid {
		var zero T
		return zero, false, err
	}
	return results[0].item, true, nil
}

// MaxBy runs the pipeline and returns the item whose key is largest.
// The second return value is false if the pipeline produced no items.
//
//	kitsune.MaxBy(ctx, users, func(u User) int { return u.Age },
//	    func(a, b int) bool { return a < b },
//	)
func MaxBy[T any, K any](ctx context.Context, p *Pipeline[T], key func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error) {
	results, err := Reduce(p, byAcc[T, K]{}, func(acc byAcc[T, K], item T) byAcc[T, K] {
		k := key(item)
		if !acc.valid || less(acc.key, k) {
			return byAcc[T, K]{item: item, key: k, valid: true}
		}
		return acc
	}).Collect(ctx, opts...)
	if err != nil || len(results) == 0 || !results[0].valid {
		var zero T
		return zero, false, err
	}
	return results[0].item, true, nil
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
	}, Concurrency(1))
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
	}, Concurrency(1))
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
	}, Concurrency(1))
	err := runner.Run(ctx, opts...)
	if errors.Is(err, errReduceWhileDone) {
		return acc, nil
	}
	return acc, err
}

// ---------------------------------------------------------------------------
// TakeRandom
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Contains / ElementAt / ToMap / SequenceEqual
// ---------------------------------------------------------------------------

// Contains runs the pipeline and returns true if any item equals value.
//
//	kitsune.Contains(ctx, kitsune.FromSlice([]int{1, 2, 3}), 2) // → true
func Contains[T comparable](ctx context.Context, p *Pipeline[T], value T, opts ...RunOption) (bool, error) {
	return p.Any(ctx, func(item T) bool { return item == value }, opts...)
}

// ElementAt runs the pipeline and returns the item at the 0-based index.
// The second return value is false if the stream is shorter than index+1.
//
//	kitsune.FromSlice([]string{"a","b","c"}).ElementAt(ctx, 1) // → "b", true
func (p *Pipeline[T]) ElementAt(ctx context.Context, index int, opts ...RunOption) (T, bool, error) {
	return p.Skip(index).First(ctx, opts...)
}

// ToMap runs the pipeline and collects items into a map[K]V.
// If multiple items map to the same key, the last value wins.
//
//	kitsune.ToMap(ctx, users, func(u User) string { return u.ID }, func(u User) string { return u.Name })
func ToMap[T any, K comparable, V any](
	ctx context.Context,
	p *Pipeline[T],
	key func(T) K,
	value func(T) V,
	opts ...RunOption,
) (map[K]V, error) {
	m := make(map[K]V)
	runner := p.ForEach(func(_ context.Context, item T) error {
		m[key(item)] = value(item)
		return nil
	}, Concurrency(1))
	if err := runner.Run(ctx, opts...); err != nil {
		return nil, err
	}
	return m, nil
}

// SequenceEqual runs both pipelines and returns true if they emit the same
// items in the same order. Both pipelines must be finite.
//
//	kitsune.SequenceEqual(ctx,
//	    kitsune.FromSlice([]int{1,2,3}),
//	    kitsune.FromSlice([]int{1,2,3}),
//	) // → true
func SequenceEqual[T comparable](ctx context.Context, a, b *Pipeline[T], opts ...RunOption) (bool, error) {
	aItems, err := a.Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	bItems, err := b.Collect(ctx, opts...)
	if err != nil {
		return false, err
	}
	return slices.Equal(aItems, bItems), nil
}

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
