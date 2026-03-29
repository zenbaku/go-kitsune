package kitsune_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

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
		kitsune.FlatMap(p, func(_ context.Context, n int, yield func(int) error) error {
			for i := range 10 {
				if err := yield(n + i); err != nil {
					return err
				}
			}
			return nil
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
		p.Dedupe(func(n int) string {
			return fmt.Sprintf("%d", n)
		}).Drain().Run(context.Background())
	}
}

func BenchmarkMapWithCache(b *testing.B) {
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
		kitsune.Map(p, func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}, kitsune.CacheBy(func(n int) string { return fmt.Sprintf("%d", n) }, kitsune.CacheBackend(cache))).Drain().Run(context.Background())
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

// BenchmarkBackpressure measures throughput under a slow consumer for each
// overflow strategy. The fast producer (FromSlice) feeds into a Map with a
// small buffer; the ForEach sink yields the CPU each item to let the buffer fill.
func BenchmarkBackpressure(b *testing.B) {
	items := makeItems(10_000)
	cases := []struct {
		name string
		opts []kitsune.StageOption
	}{
		{"Block", []kitsune.StageOption{kitsune.Buffer(4)}},
		{"DropNewest", []kitsune.StageOption{kitsune.Buffer(4), kitsune.Overflow(kitsune.DropNewest)}},
		{"DropOldest", []kitsune.StageOption{kitsune.Buffer(4), kitsune.Overflow(kitsune.DropOldest)}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsune.FromSlice(items)
				kitsune.Map(p, func(_ context.Context, n int) (int, error) {
					return n, nil
				}, tc.opts...).ForEach(func(_ context.Context, _ int) error {
					runtime.Gosched() // yield to create mild backpressure
					return nil
				}).Run(context.Background())
			}
		})
	}
}

// BenchmarkConcurrencyScaling shows how throughput scales with worker count for
// I/O-simulated work (time.Sleep). On CPU-bound work concurrency offers no gain;
// here each worker sleeps, so adding workers linearly reduces wall time.
func BenchmarkConcurrencyScaling(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping concurrency scaling benchmark in short mode")
	}
	items := makeItems(200) // smaller dataset: sleep dominates
	for _, workers := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsune.FromSlice(items)
				kitsune.Map(p, func(_ context.Context, n int) (int, error) {
					time.Sleep(50 * time.Microsecond) // simulate I/O
					return n * 2, nil
				}, kitsune.Concurrency(workers)).Drain().Run(context.Background())
			}
		})
	}
}
