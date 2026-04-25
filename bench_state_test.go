package kitsune_test

// Benchmarks for stateful operator concurrency.
//
// Run with:
//
//	go test -bench=BenchmarkMapWith -benchmem -count=3
//
// These benchmarks measure the throughput of MapWithKey and MapWith across
// serial and concurrent (sharded/worker-local) modes. The workload uses
// ref.UpdateAndGet per item — a non-trivial operation involving a mutex
// acquire/release and an integer addition — to make concurrency meaningful.
//
// Key findings to look for:
//   - MapWithKey/Concurrent* should scale near-linearly with worker count when
//     the key space is large enough to distribute evenly.
//   - MapWith/Concurrent* provides parallelism with worker-local state;
//     throughput scales with workers for CPU-bound fns.
//   - The serial baseline establishes the single-goroutine ceiling.

import (
	"context"
	"fmt"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// MapWithKey benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMapWithKey_Serial_FewKeys(b *testing.B) {
	benchMapWithKey(b, 1, 10)
}

func BenchmarkMapWithKey_Concurrent2_FewKeys(b *testing.B) {
	benchMapWithKey(b, 2, 10)
}

func BenchmarkMapWithKey_Concurrent4_FewKeys(b *testing.B) {
	benchMapWithKey(b, 4, 10)
}

func BenchmarkMapWithKey_Concurrent8_FewKeys(b *testing.B) {
	benchMapWithKey(b, 8, 10)
}

func BenchmarkMapWithKey_Serial_ManyKeys(b *testing.B) {
	benchMapWithKey(b, 1, 1000)
}

func BenchmarkMapWithKey_Concurrent2_ManyKeys(b *testing.B) {
	benchMapWithKey(b, 2, 1000)
}

func BenchmarkMapWithKey_Concurrent4_ManyKeys(b *testing.B) {
	benchMapWithKey(b, 4, 1000)
}

func BenchmarkMapWithKey_Concurrent8_ManyKeys(b *testing.B) {
	benchMapWithKey(b, 8, 1000)
}

// ---------------------------------------------------------------------------
// MapWith benchmarks (worker-local state)
// ---------------------------------------------------------------------------

func BenchmarkMapWith_Serial(b *testing.B) {
	benchMapWith(b, 1)
}

func BenchmarkMapWith_Concurrent2(b *testing.B) {
	benchMapWith(b, 2)
}

func BenchmarkMapWith_Concurrent4(b *testing.B) {
	benchMapWith(b, 4)
}

func BenchmarkMapWith_Concurrent8(b *testing.B) {
	benchMapWith(b, 8)
}

// ---------------------------------------------------------------------------
// ForEach benchmarks (terminal concurrency)
// ---------------------------------------------------------------------------

func BenchmarkForEach_Serial(b *testing.B) {
	benchForEach(b, 1)
}

func BenchmarkForEach_Concurrent4(b *testing.B) {
	benchForEach(b, 4)
}

func BenchmarkForEach_Concurrent8(b *testing.B) {
	benchForEach(b, 8)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const benchItemCount = 10_000

type benchItem struct {
	key string
	val int
}

func benchMapWithKey(b *testing.B, concurrency, numKeys int) {
	b.Helper()
	ctx := context.Background()

	items := make([]benchItem, benchItemCount)
	for i := range items {
		items[i] = benchItem{
			key: fmt.Sprintf("k%d", i%numKeys),
			val: i,
		}
	}

	totalKey := kitsune.NewKey[int]("bench_total", 0)

	var opts []kitsune.StageOption
	if concurrency > 1 {
		opts = append(opts, kitsune.Concurrency(concurrency))
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		p := kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(it benchItem) string { return it.key },
			totalKey,
			func(ctx context.Context, ref *kitsune.Ref[int], it benchItem) (int, error) {
				return ref.UpdateAndGet(ctx, func(t int) (int, error) {
					return t + it.val, nil
				})
			},
			opts...,
		)
		if _, err := p.Drain().Run(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func benchMapWith(b *testing.B, concurrency int) {
	b.Helper()
	ctx := context.Background()

	items := make([]int, benchItemCount)
	for i := range items {
		items[i] = i
	}

	counterKey := kitsune.NewKey[int]("bench_counter", 0)

	var opts []kitsune.StageOption
	if concurrency > 1 {
		opts = append(opts, kitsune.Concurrency(concurrency))
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		p := kitsune.MapWith(
			kitsune.FromSlice(items),
			counterKey,
			func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
				return ref.UpdateAndGet(ctx, func(acc int) (int, error) {
					return acc + v, nil
				})
			},
			opts...,
		)
		if _, err := p.Drain().Run(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func benchForEach(b *testing.B, concurrency int) {
	b.Helper()
	ctx := context.Background()

	items := make([]int, benchItemCount)
	for i := range items {
		items[i] = i
	}

	var opts []kitsune.StageOption
	if concurrency > 1 {
		opts = append(opts, kitsune.Concurrency(concurrency))
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		if _, err := kitsune.FromSlice(items).ForEach(func(_ context.Context, _ int) error {
			return nil
		}, opts...).Run(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// MemoryStore bypass benchmarks
// ---------------------------------------------------------------------------
//
// These benchmarks use WithStore(MemoryStore()) explicitly to exercise the
// InProcessStore fast path. Compare against BenchmarkMapWith_Serial and
// BenchmarkMapWithKey_Serial_FewKeys to confirm zero codec overhead.

func BenchmarkMapWith_MemoryStore(b *testing.B) {
	b.Helper()
	ctx := context.Background()
	items := make([]int, benchItemCount)
	for i := range items {
		items[i] = i
	}
	counterKey := kitsune.NewKey[int]("bench_ms_counter", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.MapWith(
			kitsune.FromSlice(items),
			counterKey,
			func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
				return ref.UpdateAndGet(ctx, func(acc int) (int, error) {
					return acc + v, nil
				})
			},
		)
		if _, err := p.Drain().Run(ctx, kitsune.WithStore(kitsune.MemoryStore())); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMapWithKey_Serial_FewKeys_MemoryStore(b *testing.B) {
	b.Helper()
	ctx := context.Background()
	items := make([]benchItem, benchItemCount)
	for i := range items {
		items[i] = benchItem{key: fmt.Sprintf("k%d", i%10), val: i}
	}
	totalKey := kitsune.NewKey[int]("bench_ms_total", 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(it benchItem) string { return it.key },
			totalKey,
			func(ctx context.Context, ref *kitsune.Ref[int], it benchItem) (int, error) {
				return ref.UpdateAndGet(ctx, func(t int) (int, error) {
					return t + it.val, nil
				})
			},
		)
		if _, err := p.Drain().Run(ctx, kitsune.WithStore(kitsune.MemoryStore())); err != nil {
			b.Fatal(err)
		}
	}
}
