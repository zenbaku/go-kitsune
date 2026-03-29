// Example: metricsapi — built-in MetricsHook for zero-config observability.
//
// Demonstrates: NewMetricsHook, Stage, StageMetrics, Snapshot, Throughput,
// MeanLatency, ErrorRate, WithHook, WithName.
//
// MetricsHook implements all five hook interfaces (Hook, OverflowHook,
// SupervisionHook, GraphHook, BufferHook) so a single call to WithHook
// collects processed counts, error counts, skip counts, latencies, and
// buffer fill levels without any extra wiring.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Basic per-stage metrics ---
	//
	// Pass a MetricsHook to WithHook and then read per-stage counters after
	// the pipeline finishes. Stage("name") returns a snapshot of the atomic
	// counters accumulated during the run.
	fmt.Println("=== Per-stage counters: processed, skipped, latency ===")

	m := kitsune.NewMetricsHook()

	start := time.Now()
	results, err := kitsune.Map(
		kitsune.Map(
			kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
			func(_ context.Context, n int) (int, error) { return n * 2, nil },
			kitsune.WithName("double"),
		),
		func(_ context.Context, n int) (int, error) { return n + 1, nil },
		kitsune.WithName("inc"),
	).Collect(context.Background(), kitsune.WithHook(m))
	elapsed := time.Since(start)

	if err != nil {
		panic(err)
	}
	fmt.Println("Results:", results[:5], "...")

	double := m.Stage("double")
	inc := m.Stage("inc")
	fmt.Printf("double: processed=%d  throughput=%.0f/s\n",
		double.Processed, double.Throughput(elapsed))
	fmt.Printf("inc:    processed=%d  mean_latency=%v\n",
		inc.Processed, inc.MeanLatency())

	// --- Error and skip tracking ---
	//
	// Items routed through OnError(Skip()) are counted as Skipped, not Errors.
	// Errors are only incremented when the pipeline halts (no Skip handler).
	fmt.Println("\n=== Error and skip tracking ===")

	m2 := kitsune.NewMetricsHook()

	var callN int
	_, err = kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, n int) (int, error) {
			callN++
			if callN <= 2 {
				return 0, errors.New("transient error")
			}
			return n * 10, nil
		},
		kitsune.WithName("transform"),
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background(), kitsune.WithHook(m2))
	if err != nil {
		panic(err)
	}

	s := m2.Stage("transform")
	fmt.Printf("processed=%d  skipped=%d  errors=%d  error_rate=%.2f\n",
		s.Processed, s.Skipped, s.Errors, s.ErrorRate())

	// --- Snapshot for structured export ---
	//
	// Snapshot() returns a JSON-serializable point-in-time capture of all
	// stage metrics. This is useful for logging, alerting, or dashboards.
	fmt.Println("\n=== Snapshot: all stages in one call ===")

	m3 := kitsune.NewMetricsHook()

	_, err = kitsune.Map(
		kitsune.Map(
			kitsune.FromSlice([]string{"foo", "bar", "baz", "qux"}),
			func(_ context.Context, s string) (string, error) { return "[" + s + "]", nil },
			kitsune.WithName("bracket"),
		),
		func(_ context.Context, s string) (int, error) { return len(s), nil },
		kitsune.WithName("length"),
	).Collect(context.Background(), kitsune.WithHook(m3))
	if err != nil {
		panic(err)
	}

	snap := m3.Snapshot()
	fmt.Printf("snapshot taken at: %s\n", snap.Timestamp.Format(time.RFC3339))
	for _, name := range []string{"bracket", "length"} {
		sm := snap.Stages[name]
		fmt.Printf("  %-10s processed=%d  total_ns=%d\n", name, sm.Processed, sm.TotalNs)
	}

	// --- Multi-stage pipeline with concurrency ---
	//
	// MetricsHook is safe for concurrent use. Concurrency(N) runs the stage
	// with N workers; MetricsHook aggregates their outputs via atomic counters.
	fmt.Println("\n=== Concurrent stage metrics ===")

	m4 := kitsune.NewMetricsHook()

	items := make([]int, 50)
	for i := range items {
		items[i] = i + 1
	}

	concResults, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
		kitsune.WithName("double"),
		kitsune.Concurrency(4),
		kitsune.Ordered(),
	).Collect(context.Background(), kitsune.WithHook(m4))
	if err != nil {
		panic(err)
	}

	d := m4.Stage("double")
	fmt.Printf("Processed %d items across 4 workers\n", d.Processed)
	fmt.Printf("First 5 results: %v\n", concResults[:5])
}
