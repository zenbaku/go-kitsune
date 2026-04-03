package kitsune

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// Collect — gather all items into a slice
// ---------------------------------------------------------------------------

// Collect runs the pipeline and returns all emitted items as a slice.
// It is equivalent to:
//
//	var out []T
//	err := p.ForEach(func(_ context.Context, v T) error {
//	    out = append(out, v)
//	    return nil
//	}).Run(ctx)
func Collect[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) ([]T, error) {
	var out []T
	err := p.ForEach(func(_ context.Context, v T) error {
		out = append(out, v)
		return nil
	}).Run(ctx, opts...)
	return out, err
}

// ---------------------------------------------------------------------------
// First / Last
// ---------------------------------------------------------------------------

// ErrEmpty is returned by [First], [Last], and [ElementAt] when the pipeline
// emits no items (or fewer than expected).
var ErrEmpty = errors.New("kitsune: pipeline produced no items")

// First returns the first item emitted by the pipeline and cancels processing
// immediately after. Returns [ErrEmpty] if no items are emitted.
func First[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result T
	var found bool
	// Return context.Canceled from fn to immediately stop the ForEach stage
	// without racing against the next item already buffered in the channel.
	_ = p.ForEach(func(_ context.Context, v T) error {
		result = v
		found = true
		cancel()
		return context.Canceled
	}).Run(ctx, opts...)

	if !found {
		var zero T
		return zero, ErrEmpty
	}
	return result, nil
}

// Last returns the last item emitted by the pipeline.
// Returns [ErrEmpty] if no items are emitted.
func Last[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error) {
	var result T
	var found bool
	err := p.ForEach(func(_ context.Context, v T) error {
		result = v
		found = true
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		var zero T
		return zero, err
	}
	if !found {
		var zero T
		return zero, ErrEmpty
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Count
// ---------------------------------------------------------------------------

// Count returns the number of items emitted by the pipeline.
func Count[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (int64, error) {
	var n int64
	err := p.ForEach(func(_ context.Context, _ T) error {
		n++
		return nil
	}).Run(ctx, opts...)
	return n, err
}

// ---------------------------------------------------------------------------
// Any / All
// ---------------------------------------------------------------------------

// Any returns true if at least one item satisfies pred. Processing stops
// as soon as the first matching item is found.
func Any[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := false
	_ = p.ForEach(func(_ context.Context, v T) error {
		if pred(v) {
			found = true
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return found, nil
}

// All returns true if every item satisfies pred. Processing stops as soon as
// a non-matching item is found.
func All[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	allMatch := true
	_ = p.ForEach(func(_ context.Context, v T) error {
		if !pred(v) {
			allMatch = false
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return allMatch, nil
}

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------

// Find returns the first item satisfying pred, or (zero, false, nil) if none.
func Find[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (T, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result T
	found := false
	_ = p.ForEach(func(_ context.Context, v T) error {
		if pred(v) {
			result = v
			found = true
			cancel()
			return context.Canceled
		}
		return nil
	}).Run(ctx, opts...)
	return result, found, nil
}

// ---------------------------------------------------------------------------
// Sum / Min / Max / MinMax
// ---------------------------------------------------------------------------

// Numeric is the set of integer and floating-point types supported by
// [Sum], [Min], [Max], and related operators.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64
}

// Sum returns the sum of all items. Returns 0 if the pipeline is empty.
func Sum[T Numeric](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error) {
	var sum T
	err := p.ForEach(func(_ context.Context, v T) error {
		sum += v
		return nil
	}).Run(ctx, opts...)
	return sum, err
}

// Min returns the minimum item. Returns [ErrEmpty] if no items are emitted.
func Min[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, error) {
	var result T
	found := false
	err := p.ForEach(func(_ context.Context, v T) error {
		if !found || less(v, result) {
			result = v
			found = true
		}
		return nil
	}).Run(ctx, opts...)
	if err != nil {
		var zero T
		return zero, err
	}
	if !found {
		var zero T
		return zero, ErrEmpty
	}
	return result, nil
}

// Max returns the maximum item. Returns [ErrEmpty] if no items are emitted.
func Max[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, error) {
	return Min(ctx, p, func(a, b T) bool { return less(b, a) }, opts...)
}

// ---------------------------------------------------------------------------
// ToMap
// ---------------------------------------------------------------------------

// ToMap collects items into a map using keyFn and valueFn.
// If two items produce the same key, the last one wins.
func ToMap[T any, K comparable, V any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...RunOption) (map[K]V, error) {
	m := make(map[K]V)
	err := p.ForEach(func(_ context.Context, v T) error {
		m[keyFn(v)] = valueFn(v)
		return nil
	}).Run(ctx, opts...)
	return m, err
}

// ---------------------------------------------------------------------------
// SequenceEqual
// ---------------------------------------------------------------------------

// SequenceEqual returns true if a and b emit the same items in the same order.
func SequenceEqual[T comparable](ctx context.Context, a, b *Pipeline[T], opts ...RunOption) (bool, error) {
	merged := Zip(a, b)
	equal := true
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	_ = merged.ForEach(func(_ context.Context, p Pair[T, T]) error {
		if p.Left != p.Right {
			equal = false
			cancel()
		}
		return nil
	}).Run(ctx, opts...)
	return equal, nil
}

// ---------------------------------------------------------------------------
// Iter — Go 1.23 range-over-func
// ---------------------------------------------------------------------------

// Iter returns an iterator over all items emitted by the pipeline.
// Usage (Go 1.23+):
//
//	for item := range kitsune.Iter(ctx, p) {
//	    fmt.Println(item)
//	}
//
// The iterator runs the pipeline eagerly in a background goroutine.
// Cancel ctx to stop early.
func Iter[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) func(yield func(T) bool) {
	return func(yield func(T) bool) {
		_ = p.ForEach(func(_ context.Context, v T) error {
			if !yield(v) {
				return context.Canceled
			}
			return nil
		}).Run(ctx, opts...)
	}
}
