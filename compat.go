package kitsune

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// MapIntersperse
// ---------------------------------------------------------------------------

// MapIntersperse applies fn to each item and inserts sep between consecutive
// mapped outputs (not before the first or after the last).
//
//	kitsune.MapIntersperse(words, ",", strings.ToUpper)
//	// "hello", "world" → "HELLO", ",", "WORLD"
func MapIntersperse[T, O any](p *Pipeline[T], sep O, fn func(context.Context, T) (O, error), opts ...StageOption) *Pipeline[O] {
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
	}, opts...)
}

// ---------------------------------------------------------------------------
// CountBy / SumBy
// ---------------------------------------------------------------------------

// aggregateSeq generates unique IDs for CountBy/SumBy stage names.
var aggregateSeq atomic.Int64

// CountBy emits a running snapshot of item counts per key after each input
// item. The key function must return a string. Always runs at Concurrency(1).
// Compose with [Throttle] for periodic snapshots.
//
//	kitsune.CountBy(events, func(e Event) string { return e.Type })
func CountBy[T any](p *Pipeline[T], keyFn func(T) string, opts ...StageOption) *Pipeline[map[string]int64] {
	id := aggregateSeq.Add(1)
	counts := make(map[string]int64)
	return Map(p, func(_ context.Context, item T) (map[string]int64, error) {
		counts[keyFn(item)]++
		snap := make(map[string]int64, len(counts))
		for k, v := range counts {
			snap[k] = v
		}
		return snap, nil
	}, append([]StageOption{Concurrency(1), WithName(fmt.Sprintf("count_by_%d", id))}, opts...)...)
}

// SumBy emits a running snapshot of summed values per key after each input
// item. The key function must return a string. Always runs at Concurrency(1).
// Compose with [Throttle] for periodic snapshots.
//
//	kitsune.SumBy(txns, func(t Txn) string { return t.Account }, func(t Txn) float64 { return t.Amount })
func SumBy[T any, V Numeric](p *Pipeline[T], keyFn func(T) string, valueFn func(T) V, opts ...StageOption) *Pipeline[map[string]V] {
	id := aggregateSeq.Add(1)
	sums := make(map[string]V)
	return Map(p, func(_ context.Context, item T) (map[string]V, error) {
		sums[keyFn(item)] += valueFn(item)
		snap := make(map[string]V, len(sums))
		for k, v := range sums {
			snap[k] = v
		}
		return snap, nil
	}, append([]StageOption{Concurrency(1), WithName(fmt.Sprintf("sum_by_%d", id))}, opts...)...)
}

// ---------------------------------------------------------------------------
// MapBatch
// ---------------------------------------------------------------------------

// MapBatch collects up to size items, passes the slice to fn, and flattens
// the returned slice back into individual items. Useful for bulk DB or API
// calls where batching reduces round-trips.
//
// Use [BatchTimeout] in opts to flush partial batches after a duration.
// Use [Concurrency] to process multiple batches in parallel.
//
//	kitsune.MapBatch(terms, 200, func(ctx context.Context, batch []Term) ([]Enriched, error) {
//	    return db.BulkLookup(ctx, batch)
//	})
func MapBatch[I, O any](p *Pipeline[I], size int, fn func(context.Context, []I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	batched := Batch(p, size, batchCollectOpts(opts)...)
	return FlatMap(batched, func(ctx context.Context, batch []I, yield func(O) error) error {
		results, err := fn(ctx, batch)
		if err != nil {
			return err
		}
		for _, r := range results {
			if err := yield(r); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
}

// batchCollectOpts extracts only the BatchTimeout option for the Batch stage;
// all other options (Concurrency, OnError, etc.) apply to the FlatMap stage.
func batchCollectOpts(opts []StageOption) []StageOption {
	cfg := buildStageConfig(opts)
	if cfg.batchTimeout == 0 {
		return nil
	}
	return []StageOption{BatchTimeout(cfg.batchTimeout)}
}

// ---------------------------------------------------------------------------
// Stage.Or
// ---------------------------------------------------------------------------

// Or returns a Stage that tries s first and, on error, calls fallback with the
// same input to produce a value.
//
// If both s and fallback return errors, the returned error wraps both via
// [errors.Join] so neither is silently discarded. Callers can inspect either
// cause with [errors.Is] or [errors.As].
//
//	fetch := kitsune.Stage[ID, User](func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromDB)
//	})
//	withCache := fetch.Or(func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromCache)
//	})
func (s Stage[I, O]) Or(fallback Stage[I, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		// Run each item through s first; on failure (error or no output) fall back.
		// We use Map with an item-level function that runs sub-pipelines directly.
		return Map(p, func(ctx context.Context, v I) (O, error) {
			// Use Collect (not First) so that stage errors are captured rather
			// than silently discarded.
			results, primaryErr := Collect(ctx, s(FromSlice([]I{v})))
			if primaryErr == nil && len(results) > 0 {
				return results[0], nil
			}
			// Primary produced no output (errored or empty) — try fallback.
			results, fallbackErr := Collect(ctx, fallback(FromSlice([]I{v})))
			if fallbackErr == nil && len(results) > 0 {
				return results[0], nil
			}
			var zero O
			return zero, errors.Join(primaryErr, fallbackErr)
		})
	}
}

// ---------------------------------------------------------------------------
// EndWith
// ---------------------------------------------------------------------------

// EndWith appends one or more items to p after it closes. Suffix items are
// always emitted after all items from p, in the order given.
//
//	kitsune.EndWith(kitsune.FromSlice([]int{1, 2, 3}), 4, 5)
//	// emits: 1, 2, 3, 4, 5
func EndWith[T any](p *Pipeline[T], items ...T) *Pipeline[T] {
	track(p)
	if len(items) == 0 {
		return p
	}
	itemsCopy := items
	return Concat(
		func() *Pipeline[T] { return p },
		func() *Pipeline[T] { return FromSlice(itemsCopy) },
	)
}

