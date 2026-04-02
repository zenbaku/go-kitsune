package bench_test

// BenchmarkTyped measures the experimental engine/typed generic pipeline
// against the same 3-stage workload used in the other benchmarks.
//
// The purpose is to quantify how much of Kitsune's overhead comes purely from
// any-boxing at stage boundaries vs. other framework costs (graph compilation,
// goroutine lifecycle, channel buffer management).
//
// Expected outcome: near-zero allocs/item for int workloads (int fits in the
// interface word for small values on some architectures, but on amd64/arm64 it
// always heap-escapes through chan T when T is a pointer-sized value type —
// however chan int avoids the two interface-word writes per item that chan any
// requires). In practice we expect 0 allocs/item vs. the current ~2.

import (
	"fmt"
	"testing"

	"github.com/jonathan/go-kitsune/engine/typed"
)

// BenchmarkTyped runs the same Map → Filter → Drain pipeline as BenchmarkKitsune
// but uses the experimental typed engine with chan int instead of chan any.
func BenchmarkTyped(b *testing.B) {
	for _, n := range datasets {
		items := makeItems(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				p := typed.FromSlice(items)
				mapped := typed.Map(p, pipeMap)
				filtered := typed.Filter(mapped, pipeKeep)
				filtered.Drain(b.Context()) //nolint:errcheck
			}
			b.ReportMetric(itemsPerSec(b, n), "items/sec")
		})
	}
}
