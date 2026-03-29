package kitsune_test

import (
	"context"
	"sort"
	"testing"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/testkit"
)

// TestLatencyPercentiles measures per-item processing latency distributions for
// three representative scenarios. Unlike throughput benchmarks (b.N loops), these
// tests collect every item's duration from RecordingHook and compute p50/p95/p99.
//
// Run with:
//
//	go test -v -run TestLatencyPercentiles ./...
func TestLatencyPercentiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency percentile tests in short mode")
	}

	t.Run("MapLinear", func(t *testing.T) {
		items := makeItems(10_000)
		p := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}, kitsune.WithName("map"))

		_, hook := testkit.MustCollectWithHook(t, p, kitsune.WithSampleRate(-1))
		reportPercentiles(t, "MapLinear (CPU-trivial, 1 worker)", durationsFrom(hook, "map"))
	})

	t.Run("MapConcurrent", func(t *testing.T) {
		items := makeItems(10_000)
		p := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, n int) (int, error) {
			return n * 2, nil
		}, kitsune.WithName("map"), kitsune.Concurrency(4))

		_, hook := testkit.MustCollectWithHook(t, p, kitsune.WithSampleRate(-1))
		reportPercentiles(t, "MapConcurrent (CPU-trivial, 4 workers)", durationsFrom(hook, "map"))
	})

	t.Run("IOSimulated", func(t *testing.T) {
		items := makeItems(200)
		p := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, n int) (int, error) {
			time.Sleep(100 * time.Microsecond) // simulate I/O
			return n * 2, nil
		}, kitsune.WithName("map"), kitsune.Concurrency(4))

		_, hook := testkit.MustCollectWithHook(t, p, kitsune.WithSampleRate(-1))
		reportPercentiles(t, "IOSimulated (100µs sleep, 4 workers)", durationsFrom(hook, "map"))
	})
}

// durationsFrom extracts per-item processing durations for the named stage.
func durationsFrom(hook *testkit.RecordingHook, stage string) []time.Duration {
	events := hook.ItemsFor(stage)
	durations := make([]time.Duration, 0, len(events))
	for _, e := range events {
		if e.Err == nil && !e.IsSample {
			durations = append(durations, e.Duration)
		}
	}
	return durations
}

// reportPercentiles logs a percentile table for the given durations.
func reportPercentiles(t *testing.T, label string, durations []time.Duration) {
	t.Helper()
	if len(durations) == 0 {
		t.Logf("%s: no durations recorded", label)
		return
	}
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p := func(pct float64) time.Duration {
		idx := int(float64(len(sorted)-1) * pct / 100)
		return sorted[idx]
	}

	t.Logf("%-52s  n=%d  p50=%-8v  p95=%-8v  p99=%-8v  max=%v",
		label, len(sorted), p(50), p(95), p(99), sorted[len(sorted)-1])
}
