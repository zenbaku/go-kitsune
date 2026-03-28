package kitsune

import (
	"context"
	"math"
)

// ChunkBy groups consecutive items that produce the same key into slices.
// A new chunk begins each time fn returns a value different from the previous item.
//
//	kitsune.ChunkBy(kitsune.FromSlice([]int{1, 1, 2, 2, 3, 1}), func(n int) int { return n })
//	// → [1 1], [2 2], [3], [1]
//
// ChunkBy buffers the entire stream in memory before emitting — use only on
// bounded (finite) pipelines.
func ChunkBy[T any, K comparable](p *Pipeline[T], fn func(T) K) *Pipeline[[]T] {
	return FlatMap(Batch(p, math.MaxInt), func(_ context.Context, items []T) ([][]T, error) {
		if len(items) == 0 {
			return nil, nil
		}
		var chunks [][]T
		var cur []T
		curKey := fn(items[0])
		for _, item := range items {
			k := fn(item)
			if k != curKey {
				chunks = append(chunks, cur)
				cur = nil
				curKey = k
			}
			cur = append(cur, item)
		}
		if len(cur) > 0 {
			chunks = append(chunks, cur)
		}
		return chunks, nil
	})
}

// ChunkWhile groups consecutive items into chunks as long as fn(prev, next) returns true.
// A new chunk begins whenever fn returns false for an adjacent pair.
//
//	kitsune.ChunkWhile(kitsune.FromSlice([]int{1, 2, 4, 9, 10, 11, 15}),
//	    func(prev, next int) bool { return next-prev <= 1 },
//	)
//	// → [1 2], [4], [9 10 11], [15]
//
// ChunkWhile buffers the entire stream in memory before emitting — use only on
// bounded (finite) pipelines.
func ChunkWhile[T any](p *Pipeline[T], fn func(prev, next T) bool) *Pipeline[[]T] {
	return FlatMap(Batch(p, math.MaxInt), func(_ context.Context, items []T) ([][]T, error) {
		if len(items) == 0 {
			return nil, nil
		}
		var chunks [][]T
		cur := []T{items[0]}
		for i := 1; i < len(items); i++ {
			if fn(items[i-1], items[i]) {
				cur = append(cur, items[i])
			} else {
				chunks = append(chunks, cur)
				cur = []T{items[i]}
			}
		}
		if len(cur) > 0 {
			chunks = append(chunks, cur)
		}
		return chunks, nil
	})
}
