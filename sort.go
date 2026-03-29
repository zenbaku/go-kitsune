package kitsune

import (
	"context"
	"math"
	"slices"
)

// Sort collects all items and emits them in the order determined by less.
// less(a, b) should return true when a should appear before b in the output.
//
//	kitsune.Sort(p, func(a, b int) bool { return a < b })
//
// Sort buffers the entire stream in memory — use only on bounded (finite) pipelines.
func Sort[T any](p *Pipeline[T], less func(a, b T) bool) *Pipeline[T] {
	return FlatMap(Batch(p, math.MaxInt), func(_ context.Context, items []T, yield func(T) error) error {
		sorted := make([]T, len(items))
		copy(sorted, items)
		slices.SortFunc(sorted, func(a, b T) int {
			if less(a, b) {
				return -1
			}
			if less(b, a) {
				return 1
			}
			return 0
		})
		for _, item := range sorted {
			if err := yield(item); err != nil {
				return err
			}
		}
		return nil
	})
}

// SortBy collects all items and emits them sorted by a derived key.
// less(a, b) should return true when key a should appear before key b.
//
//	kitsune.SortBy(users, func(u User) string { return u.Name },
//	    func(a, b string) bool { return a < b },
//	)
//
// SortBy buffers the entire stream in memory — use only on bounded (finite) pipelines.
func SortBy[T any, K any](p *Pipeline[T], key func(T) K, less func(a, b K) bool) *Pipeline[T] {
	return FlatMap(Batch(p, math.MaxInt), func(_ context.Context, items []T, yield func(T) error) error {
		sorted := make([]T, len(items))
		copy(sorted, items)
		slices.SortFunc(sorted, func(a, b T) int {
			ka, kb := key(a), key(b)
			if less(ka, kb) {
				return -1
			}
			if less(kb, ka) {
				return 1
			}
			return 0
		})
		for _, item := range sorted {
			if err := yield(item); err != nil {
				return err
			}
		}
		return nil
	})
}
