package kitsune

import (
	"context"
	"iter"
)

// ---------------------------------------------------------------------------
// Terminal methods on Pipeline[T]
//
// These are method aliases for the free-function terminals, matching the v1
// API where terminals were methods on *Pipeline[T]. The free functions remain
// the canonical implementation; these methods simply delegate to them.
//
// Note: Go does not allow methods with their own type parameters, so the
// following terminals must remain free functions only:
//   ToMap, GroupBy, FrequenciesBy, MinBy, MaxBy, TakeRandom, SequenceEqual
// ---------------------------------------------------------------------------

// Collect runs the pipeline and returns all emitted items as a slice.
func (p *Pipeline[T]) Collect(ctx context.Context, opts ...RunOption) ([]T, error) {
	return Collect(ctx, p, opts...)
}

// First returns the first item emitted by the pipeline.
// Returns (zero, false, nil) if the pipeline emits no items.
func (p *Pipeline[T]) First(ctx context.Context, opts ...RunOption) (T, bool, error) {
	return First(ctx, p, opts...)
}

// Last returns the last item emitted by the pipeline.
// Returns (zero, false, nil) if the pipeline emits no items.
func (p *Pipeline[T]) Last(ctx context.Context, opts ...RunOption) (T, bool, error) {
	return Last(ctx, p, opts...)
}

// Count runs the pipeline and returns the number of items emitted.
func (p *Pipeline[T]) Count(ctx context.Context, opts ...RunOption) (int64, error) {
	return Count(ctx, p, opts...)
}

// Any returns true if at least one item satisfies pred.
func (p *Pipeline[T]) Any(ctx context.Context, pred func(T) bool, opts ...RunOption) (bool, error) {
	return Any(ctx, p, pred, opts...)
}

// All returns true if every item satisfies pred.
func (p *Pipeline[T]) All(ctx context.Context, pred func(T) bool, opts ...RunOption) (bool, error) {
	return All(ctx, p, pred, opts...)
}

// Find returns the first item satisfying pred, or (zero, false, nil) if none.
func (p *Pipeline[T]) Find(ctx context.Context, pred func(T) bool, opts ...RunOption) (T, bool, error) {
	return Find(ctx, p, pred, opts...)
}

// ReduceWhile folds items until fn signals stop.
func (p *Pipeline[T]) ReduceWhile(ctx context.Context, initial T, fn func(T, T) (T, bool), opts ...RunOption) (T, error) {
	return ReduceWhile(ctx, p, initial, fn, opts...)
}

// Iter returns a Go 1.23 iterator over the pipeline's items.
// The second return value is a function that must be called after the iterator
// is exhausted (or abandoned) to retrieve any pipeline error.
func (p *Pipeline[T]) Iter(ctx context.Context, opts ...RunOption) (iter.Seq[T], func() error) {
	return Iter(ctx, p, opts...)
}

// ---------------------------------------------------------------------------
// Operator methods on Pipeline[T]
//
// Same-type operator shortcuts matching the v1 method API. These delegate to
// the free-function forms; the free functions remain the canonical implementation.
// ---------------------------------------------------------------------------

// Take emits at most n items then stops.
func (p *Pipeline[T]) Take(n int) *Pipeline[T] {
	return Take(p, n)
}

// Drop discards the first n items then forwards the rest.
func (p *Pipeline[T]) Drop(n int) *Pipeline[T] {
	return Drop(p, n)
}

// Skip discards the first n items then forwards the rest.
// Alias for [Drop]; retained for v1 API compatibility.
func (p *Pipeline[T]) Skip(n int) *Pipeline[T] { return Drop(p, n) }

// Filter forwards only items for which fn returns true.
// fn is a plain predicate (no context, no error); use the free-function
// [Filter] directly for the context-aware form.
func (p *Pipeline[T]) Filter(fn func(T) bool, opts ...StageOption) *Pipeline[T] {
	return Filter(p, FilterFunc(fn), opts...)
}

// Reject discards items for which fn returns true (inverse of Filter).
func (p *Pipeline[T]) Reject(fn func(T) bool, opts ...StageOption) *Pipeline[T] {
	return Reject(p, RejectFunc(fn), opts...)
}

// Tap calls fn for each item as a side-effect, forwarding items unchanged.
// fn is a plain side-effect function (no context); use the free-function
// [Tap] directly for the context-aware form.
func (p *Pipeline[T]) Tap(fn func(T), opts ...StageOption) *Pipeline[T] {
	return Tap(p, TapFunc(fn), opts...)
}

// TapError calls fn as a side-effect when the pipeline terminates with an
// error, then re-propagates the error unchanged.
// fn is a plain error-observer function (no context); use the free-function
// [TapError] directly for the context-aware form.
func (p *Pipeline[T]) TapError(fn func(error)) *Pipeline[T] {
	return TapError(p, func(_ context.Context, err error) { fn(err) })
}

// Finally calls fn as a side-effect when the pipeline exits for any reason,
// then re-propagates the outcome unchanged.
// fn is a plain callback (no context); use the free-function [Finally]
// directly for the context-aware form.
func (p *Pipeline[T]) Finally(fn func(error)) *Pipeline[T] {
	return Finally(p, func(_ context.Context, err error) { fn(err) })
}

// IgnoreElements drains p for its side effects and emits nothing downstream.
// The returned pipeline completes when p completes.
func (p *Pipeline[T]) IgnoreElements() *Pipeline[T] {
	return IgnoreElements(p)
}

// Dedupe drops items whose key (returned by keyFn) has already been seen.
// Uses a global in-process [MemoryDedupSet] unless [WithDedupSet] is provided.
func (p *Pipeline[T]) Dedupe(keyFn func(T) string, opts ...StageOption) *Pipeline[T] {
	// Inject a MemoryDedupSet for global dedup semantics (matching v1 behavior)
	// unless the caller already supplied one via WithDedupSet.
	hasSet := false
	for _, opt := range opts {
		cfg := &stageConfig{}
		opt(cfg)
		if cfg.dedupSet != nil {
			hasSet = true
			break
		}
	}
	if !hasSet {
		opts = append([]StageOption{WithDedupSet(MemoryDedupSet())}, opts...)
	}
	return DedupeBy(p, keyFn, opts...)
}
