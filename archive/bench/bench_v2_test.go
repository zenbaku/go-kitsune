package bench_test

// v2 benchmarks — compare the typed generic engine (chan T) against v1 (chan any)
// and the raw-goroutine baseline across three workload tiers:
//
//   Trivial    — n*2 map + n%3 filter. Framework overhead dominates; the key
//                metric is allocs/op. v2 eliminates the two interface-word writes
//                per item that v1's chan any requires, so we expect a meaningful
//                drop in allocs/op and a corresponding throughput improvement.
//
//   Light CPU  — SHA-256 per item (~300 ns on M1). Work and framework overhead
//                are in the same order; boxing cost is a smaller fraction.
//
//   I/O bound  — 1 µs sleep per item. Framework overhead is negligible; both
//                engines should look identical here.
//
// Naming convention: matching benchmark pairs are named *Kitsune / *KitsuneV2
// so benchstat can diff them directly:
//
//	go test -bench=. -benchmem -count=10 . | tee results.txt
//	benchstat -col /engine results.txt

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	kitsunev2 "github.com/zenbaku/go-kitsune/v2"
)

// ---------------------------------------------------------------------------
// Trivial tier  (n*2 map + n%3 filter, framework-overhead-dominated)
// ---------------------------------------------------------------------------

// BenchmarkKitsuneV2 runs the same trivial 3-stage pipeline as BenchmarkKitsune
// but through the v2 typed engine. The key comparison is allocs/op:
// v1 boxes every int into an interface{} twice (once on send, once on receive)
// for each inter-stage hop; v2 uses chan int with zero boxing.
func BenchmarkKitsuneV2(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsunev2.FromSlice(items)
				mapped := kitsunev2.Map(p, func(_ context.Context, v int) (int, error) {
					return pipeMap(v), nil
				})
				kitsunev2.Filter(mapped, func(_ context.Context, v int) (bool, error) {
					return pipeKeep(v), nil
				}).Drain().Run(context.Background()) //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// Light CPU tier  (SHA-256 per item, ~300 ns)
// ---------------------------------------------------------------------------

func lightCPUWorkV2(n int) int {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(n))
	h := sha256.Sum256(buf[:])
	return int(binary.LittleEndian.Uint64(h[:8]))
}

// BenchmarkLightCPUKitsuneV2 mirrors BenchmarkLightCPUKitsune for the v2 engine.
func BenchmarkLightCPUKitsuneV2(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsunev2.FromSlice(items)
				kitsunev2.Filter(
					kitsunev2.Map(p, func(_ context.Context, v int) (int, error) {
						return lightCPUWorkV2(v), nil
					}),
					func(_ context.Context, v int) (bool, error) {
						return pipeKeep(v), nil
					},
				).Drain().Run(context.Background()) //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// I/O-bound tier  (1 µs sleep per item in the Map stage)
// ---------------------------------------------------------------------------

// BenchmarkIOBoundKitsuneV2 mirrors BenchmarkIOBoundKitsune for the v2 engine.
func BenchmarkIOBoundKitsuneV2(b *testing.B) {
	for _, n := range ioDatasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsunev2.FromSlice(items)
				kitsunev2.Filter(
					kitsunev2.Map(p, func(_ context.Context, v int) (int, error) {
						time.Sleep(time.Microsecond)
						return pipeMap(v), nil
					}),
					func(_ context.Context, v int) (bool, error) {
						return pipeKeep(v), nil
					},
				).Drain().Run(context.Background()) //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}
