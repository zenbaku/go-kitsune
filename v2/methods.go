package kitsune

import "context"

// ---------------------------------------------------------------------------
// Terminal methods on Pipeline[T]
//
// These are method aliases for the free-function terminals, matching the v1
// API where terminals were methods on *Pipeline[T]. The free functions remain
// the canonical implementation; these methods simply delegate to them.
//
// Note: Go does not allow methods with their own type parameters, so the
// following terminals must remain free functions only:
//   ToMap, GroupBy, FrequenciesBy, MinBy, MaxBy, Iter, TakeRandom, SequenceEqual
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
