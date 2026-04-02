package bench_test

// Realistic workload benchmarks — complement the trivial (n*2, n%3) baseline
// with two tiers that better represent real pipeline usage:
//
//   Light CPU  — SHA-256 per item (~300 ns work on M1). The framework overhead
//                (~180 ns/item) is in the same order of magnitude as the work,
//                so the cost is visible but not dominant. Represents data
//                transformation pipelines (hashing, encoding, validation).
//
//   I/O bound  — 1 µs sleep per item. Represents stages that call external
//                systems (HTTP, database, gRPC). Shows that framework overhead
//                becomes negligible relative to stage latency.
//
// Each tier benchmarks raw goroutines alongside Kitsune so the overhead
// percentage is directly readable from the ns/op numbers.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"golang.org/x/sync/errgroup"
)

// lightCPUWork computes SHA-256 of the item value, returning a derived int.
// Cost is ~300 ns on Apple M1 — chosen because it sits in the same order of
// magnitude as Kitsune's per-item overhead, making the crossover point clear.
func lightCPUWork(n int) int {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(n))
	h := sha256.Sum256(buf[:])
	return int(binary.LittleEndian.Uint64(h[:8]))
}

// ---------------------------------------------------------------------------
// Light CPU tier  (SHA-256 per item, ~300 ns)
// ---------------------------------------------------------------------------

// BenchmarkLightCPURaw is the raw-goroutine baseline for light-CPU work.
func BenchmarkLightCPURaw(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				ch1 := make(chan int, bufSize)
				ch2 := make(chan int, bufSize)
				ch3 := make(chan int, bufSize)
				var eg errgroup.Group
				eg.Go(func() error {
					for _, v := range items {
						ch1 <- v
					}
					close(ch1)
					return nil
				})
				eg.Go(func() error {
					for v := range ch1 {
						ch2 <- lightCPUWork(v)
					}
					close(ch2)
					return nil
				})
				eg.Go(func() error {
					for v := range ch2 {
						if pipeKeep(v) {
							ch3 <- v
						}
					}
					close(ch3)
					return nil
				})
				eg.Go(func() error {
					//nolint:revive
					for range ch3 {
					}
					return nil
				})
				eg.Wait() //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// BenchmarkLightCPUKitsune runs the same light-CPU pipeline through Kitsune.
func BenchmarkLightCPUKitsune(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsune.FromSlice(items)
				kitsune.Map(p, func(_ context.Context, v int) (int, error) {
					return lightCPUWork(v), nil
				}).Filter(pipeKeep).Drain().Run(context.Background())
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// I/O-bound tier  (1 µs sleep per item in the Map stage)
// ---------------------------------------------------------------------------

// ioDatasets is smaller than the trivial suite: at 1 µs/item, 1 M items would
// take ~1 s per benchmark run, making count=5 impractical.
var ioDatasets = []int{1_000, 10_000}

// BenchmarkIOBoundRaw is the raw-goroutine baseline for I/O-bound work.
func BenchmarkIOBoundRaw(b *testing.B) {
	for _, n := range ioDatasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				ch1 := make(chan int, bufSize)
				ch2 := make(chan int, bufSize)
				ch3 := make(chan int, bufSize)
				var eg errgroup.Group
				eg.Go(func() error {
					for _, v := range items {
						ch1 <- v
					}
					close(ch1)
					return nil
				})
				eg.Go(func() error {
					for v := range ch1 {
						time.Sleep(time.Microsecond)
						ch2 <- pipeMap(v)
					}
					close(ch2)
					return nil
				})
				eg.Go(func() error {
					for v := range ch2 {
						if pipeKeep(v) {
							ch3 <- v
						}
					}
					close(ch3)
					return nil
				})
				eg.Go(func() error {
					//nolint:revive
					for range ch3 {
					}
					return nil
				})
				eg.Wait() //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// BenchmarkIOBoundKitsune runs the same I/O-bound pipeline through Kitsune.
func BenchmarkIOBoundKitsune(b *testing.B) {
	for _, n := range ioDatasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsune.FromSlice(items)
				kitsune.Map(p, func(_ context.Context, v int) (int, error) {
					time.Sleep(time.Microsecond)
					return pipeMap(v), nil
				}).Filter(pipeKeep).Drain().Run(context.Background())
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}
