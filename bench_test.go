package kitsune_test

import (
	"context"
	"fmt"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
)

func makeItems(n int) []int {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	return items
}

func BenchmarkMapLinear(b *testing.B) {
	items := makeItems(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		kitsune.Map(p, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}).Drain().Run(context.Background())
	}
}

func BenchmarkMapConcurrent(b *testing.B) {
	items := makeItems(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		kitsune.Map(p, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}, kitsune.Concurrency(4)).Drain().Run(context.Background())
	}
}

func BenchmarkFlatMap(b *testing.B) {
	items := makeItems(1_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		kitsune.FlatMap(p, func(_ context.Context, n int) ([]int, error) {
			return []int{n, n + 1, n + 2, n + 3, n + 4, n + 5, n + 6, n + 7, n + 8, n + 9}, nil
		}).Drain().Run(context.Background())
	}
}

func BenchmarkBatchUnbatch(b *testing.B) {
	items := makeItems(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		batched := kitsune.Batch(p, 100)
		kitsune.Unbatch(batched).Drain().Run(context.Background())
	}
}

func BenchmarkFilter(b *testing.B) {
	items := makeItems(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		p.Filter(func(n int) bool { return n%2 == 0 }).Drain().Run(context.Background())
	}
}

func BenchmarkDedupe(b *testing.B) {
	// 10k items with 50% duplicates.
	items := makeItems(10_000)
	for i := range items {
		items[i] = i % 5000
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.FromSlice(items)
		kitsune.Dedupe(p, func(n int) string {
			return fmt.Sprintf("%d", n)
		}, kitsune.MemoryDedupSet()).Drain().Run(context.Background())
	}
}

func BenchmarkCachedMap(b *testing.B) {
	// 10k items, 1k unique keys → 90% hit rate after warmup.
	items := makeItems(10_000)
	for i := range items {
		items[i] = i % 1000
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		cache := kitsune.MemoryCache(2000)
		p := kitsune.FromSlice(items)
		kitsune.CachedMap(p, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}, func(n int) string { return fmt.Sprintf("%d", n) }, cache, 0).Drain().Run(context.Background())
	}
}

func BenchmarkMapOrdered(b *testing.B) {
	// Measures throughput of Concurrency(8) + Ordered() vs plain Concurrency(8).
	items := makeItems(10_000)
	b.Run("ordered", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			p := kitsune.FromSlice(items)
			kitsune.Map(p, func(_ context.Context, n int) (int, error) {
				return n * 2, nil
			}, kitsune.Concurrency(8), kitsune.Ordered()).Drain().Run(context.Background())
		}
	})
	b.Run("unordered", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			p := kitsune.FromSlice(items)
			kitsune.Map(p, func(_ context.Context, n int) (int, error) {
				return n * 2, nil
			}, kitsune.Concurrency(8)).Drain().Run(context.Background())
		}
	})
}
