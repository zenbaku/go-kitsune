package kitsune

import (
	"context"
	"fmt"
	"sync/atomic"
)

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


