// Example: metrics — custom Hook that collects per-stage telemetry.
//
// Demonstrates:
//   - Implementing the kitsune.Hook interface from scratch
//   - Collecting per-stage item counts, error counts, and latency
//   - Using sync.Map + atomic.Int64 for lock-free metric storage
//   - Printing a formatted summary table after the pipeline completes
//   - WithHook, WithName, and Concurrency working together
package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

// stageMetrics holds counters for a single named stage.
type stageMetrics struct {
	items   atomic.Int64
	errors  atomic.Int64
	totalNs atomic.Int64
}

// MetricsHook implements kitsune.Hook and accumulates per-stage stats.
type MetricsHook struct {
	stages sync.Map // map[string]*stageMetrics
}

func (h *MetricsHook) get(stage string) *stageMetrics {
	v, _ := h.stages.LoadOrStore(stage, &stageMetrics{})
	return v.(*stageMetrics)
}

func (h *MetricsHook) OnStageStart(_ context.Context, _ string) {}

func (h *MetricsHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	m := h.get(stage)
	m.items.Add(1)
	m.totalNs.Add(dur.Nanoseconds())
	if err != nil {
		m.errors.Add(1)
	}
}

func (h *MetricsHook) OnStageDone(_ context.Context, stage string, processed int64, errors int64) {
	fmt.Printf("  stage %-12s done  processed=%d errors=%d\n", stage, processed, errors)
}

// Summary prints a formatted table of all stages sorted by insertion order.
func (h *MetricsHook) Summary() {
	fmt.Println()
	fmt.Printf("%-14s  %8s  %8s  %12s\n", "STAGE", "ITEMS", "ERRORS", "AVG LATENCY")
	fmt.Println("----------------------------------------------")
	h.stages.Range(func(key, val any) bool {
		stage := key.(string)
		m := val.(*stageMetrics)
		items := m.items.Load()
		errs := m.errors.Load()
		ns := m.totalNs.Load()
		var avg time.Duration
		if items > 0 {
			avg = time.Duration(ns / items)
		}
		fmt.Printf("%-14s  %8d  %8d  %12s\n", stage, items, errs, avg)
		return true
	})
}

func main() {
	const total = 1000

	src := make([]int, total)
	for i := range src {
		src[i] = i
	}

	hook := &MetricsHook{}

	// Stage: parse — light transformation, rename item to string token.
	parsed := kitsune.Map(
		kitsune.FromSlice(src),
		func(_ context.Context, n int) (int, error) {
			// simulate a tiny parse step
			return n * 2, nil
		},
		kitsune.WithName("parse"),
	)

	// Stage: enrich — simulate occasional I/O with 4 concurrent workers.
	enriched := kitsune.Map(
		parsed,
		func(_ context.Context, n int) (int, error) {
			if n%50 == 0 {
				time.Sleep(time.Millisecond) // simulate I/O on some items
			}
			return n + 1, nil
		},
		kitsune.Concurrency(4),
		kitsune.WithName("enrich"),
	)

	// Stage: validate — keep ~70% of items (drop those divisible by 10 or 7).
	// Filter accepts no options, so we use Map + OnError(Skip()) to attach a name.
	errDropped := fmt.Errorf("dropped")
	validated := kitsune.Map(
		enriched,
		func(_ context.Context, n int) (int, error) {
			if n%10 == 0 || n%7 == 0 {
				return 0, errDropped
			}
			return n, nil
		},
		kitsune.WithName("validate"),
		kitsune.OnError(kitsune.Skip()),
	)

	// Sink: count results.
	var count int64
	sink := validated.ForEach(func(_ context.Context, _ int) error {
		atomic.AddInt64(&count, 1)
		return nil
	}, kitsune.WithName("sink"))

	fmt.Println("Running pipeline (1000 items, 4 stages)...")
	fmt.Println()

	if err := sink.Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		panic(err)
	}

	fmt.Printf("\nSink received %d items\n", count)

	hook.Summary()
}
