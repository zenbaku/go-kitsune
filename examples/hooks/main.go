// Example: hooks: observability with LogHook and MetricsHook.
//
// Demonstrates: WithHook, LogHook, MetricsHook, MetricsSnapshot.JSON,
// MultiHook, StageMetrics.AvgLatency
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	metrics := kitsune.NewMetricsHook()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	slow := func(_ context.Context, n int) (int, error) {
		time.Sleep(2 * time.Millisecond)
		return n * n, nil
	}

	items := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8})

	squared := kitsune.Map(items, slow, kitsune.WithName("square"))
	filtered := kitsune.Filter(squared,
		func(_ context.Context, n int) (bool, error) { return n > 10, nil },
		kitsune.WithName("filter"),
	)

	results, err := kitsune.Collect(ctx, filtered,
		// MultiHook combines an arbitrary number of hooks.
		kitsune.WithHook(kitsune.MultiHook(metrics, kitsune.LogHook(logger))),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("\nresults:", results)

	// --- Inspect per-stage metrics ---

	fmt.Println("\n=== Stage Metrics ===")
	snap := metrics.Snapshot()
	for _, s := range snap.Stages {
		fmt.Printf("  %-12s  processed=%d  errors=%d  avgLatency=%v\n",
			s.Stage, s.Processed, s.Errors, s.AvgLatency().Round(time.Microsecond))
	}

	// --- JSON serialisation ---

	j, _ := snap.JSON()
	_ = j // would write to monitoring system
	fmt.Printf("\nJSON snapshot: %d bytes\n", len(j))
}
