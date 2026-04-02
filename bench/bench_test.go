// Package bench compares Kitsune's 3-stage pipeline throughput against
// a hand-rolled goroutine pipeline, sourcegraph/conc, and reugn/go-streams.
//
// Run comparison benchmarks:
//
//	go test -bench=. -benchmem -count=5 -timeout 300s ./...
//
// For stable statistics across runs use benchstat:
//
//	go test -bench=. -benchmem -count=10 ./... | tee results.txt && benchstat results.txt
package bench_test

import (
	"context"
	"fmt"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/reugn/go-streams/extension"
	gsflow "github.com/reugn/go-streams/flow"
	"github.com/sourcegraph/conc"
	"golang.org/x/sync/errgroup"
)

// bufSize matches engine.DefaultBuffer so channel-based pipelines use the
// same backpressure budget as Kitsune.
const bufSize = 16

// datasets are the benchmark sizes. Larger sizes amortise setup cost;
// 1M is the primary comparison point cited in the documentation.
var datasets = []int{10_000, 100_000, 1_000_000}

func makeItems(n int) []int {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	return items
}

// The three pipeline stages applied identically across all implementations:
//
//	Stage 1 — Map:    double the value
//	Stage 2 — Filter: keep items not divisible by 3 (~67% pass-through)
//	Stage 3 — Drain:  consume and discard (atomic accumulate in channel impls
//	                  to prevent dead-code elimination)
func pipeMap(n int) int  { return n * 2 }
func pipeKeep(n int) bool { return n%3 != 0 }

// ---------------------------------------------------------------------------
// Raw goroutines
// ---------------------------------------------------------------------------

// BenchmarkRawGoroutines is the raw baseline: four goroutines (source, map,
// filter, drain) connected by three buffered channels using errgroup for
// structured lifecycle management.
func BenchmarkRawGoroutines(b *testing.B) {
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
					//nolint:revive // intentionally drain without using the value
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

// ---------------------------------------------------------------------------
// Kitsune
// ---------------------------------------------------------------------------

// BenchmarkKitsune runs the same 3-stage pipeline using Kitsune operators.
func BenchmarkKitsune(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := kitsune.FromSlice(items)
				mapped := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
					return pipeMap(v), nil
				})
				mapped.Filter(pipeKeep).Drain().Run(context.Background())
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// sourcegraph/conc
// ---------------------------------------------------------------------------

// BenchmarkConc implements the same 4-goroutine channel pipeline using
// sourcegraph/conc's WaitGroup for lifecycle and panic propagation.
// conc is a structured-concurrency primitive, not a pipeline library — the
// channels and routing logic are still hand-rolled.
func BenchmarkConc(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				ch1 := make(chan int, bufSize)
				ch2 := make(chan int, bufSize)
				ch3 := make(chan int, bufSize)
				var wg conc.WaitGroup
				wg.Go(func() {
					for _, v := range items {
						ch1 <- v
					}
					close(ch1)
				})
				wg.Go(func() {
					for v := range ch1 {
						ch2 <- pipeMap(v)
					}
					close(ch2)
				})
				wg.Go(func() {
					for v := range ch2 {
						if pipeKeep(v) {
							ch3 <- v
						}
					}
					close(ch3)
				})
				wg.Go(func() {
					//nolint:revive
					for range ch3 {
					}
				})
				wg.Wait()
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// reugn/go-streams
// ---------------------------------------------------------------------------

// BenchmarkGoStreams implements the pipeline using reugn/go-streams.
// Note: go-streams uses chan any throughout, so all items are heap-boxed as
// any before entering the pipeline. The source channel must be pre-filled
// and closed before the pipeline runs (no slice source built-in), adding
// one extra allocation of O(N) for the source channel.
func BenchmarkGoStreams(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				// Pre-fill a buffered source channel. go-streams has no
				// built-in slice source, so this boxing cost is inherent.
				inCh := make(chan any, n)
				for _, v := range items {
					inCh <- v
				}
				close(inCh)

				src := extension.NewChanSource(inCh)
				mapF := gsflow.NewMap(func(v int) int { return pipeMap(v) }, 1)
				filterF := gsflow.NewFilter(func(v int) bool { return pipeKeep(v) }, 1)
				sink := extension.NewIgnoreSink()

				src.Via(mapF).Via(filterF).To(sink)
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func itemsPerSec(b *testing.B, n int) float64 {
	b.Helper()
	ns := b.Elapsed().Nanoseconds()
	if ns == 0 {
		return 0
	}
	return float64(b.N*n) / float64(ns) * 1e9
}
