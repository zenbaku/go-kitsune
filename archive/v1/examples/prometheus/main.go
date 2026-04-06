// Example: prometheus — attaching a Prometheus hook to a kitsune pipeline.
//
// Demonstrates: kprometheus.New (hook), kitsune.WithHook, per-stage counters,
// duration histograms, overflow drops, and supervision restarts.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kprometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	ctx := context.Background()

	// Create a fresh Prometheus registry and attach a kprometheus hook.
	reg := prometheus.NewRegistry()
	hook := kprometheus.New(reg, "myapp")

	// --- Example 1: normal processing ---
	fmt.Println("=== Normal processing ===")
	results, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, v int) (int, error) {
			time.Sleep(time.Millisecond) // simulate work
			return v * v, nil
		},
		kitsune.WithName("square"),
	).Collect(ctx, kitsune.WithHook(hook))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  squared: %v\n", results)
	printMetric(reg, "myapp_stage_items_total")
	printMetric(reg, "myapp_stage_duration_seconds")

	// --- Example 2: errors and skips ---
	fmt.Println("\n=== Errors and skips ===")
	boom := errors.New("boom")
	_ = kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v, nil
		},
		kitsune.OnError(kitsune.Skip()),
		kitsune.WithName("skip-evens"),
	).ForEach(func(_ context.Context, v int) error { return nil }).
		Run(ctx, kitsune.WithHook(hook))
	printMetric(reg, "myapp_stage_items_total")

	// --- Example 3: overflow drops ---
	fmt.Println("\n=== Overflow drops ===")
	items := make([]int, 500)
	for i := range items {
		items[i] = i
	}
	_ = kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v, nil },
		kitsune.Buffer(2),
		kitsune.Overflow(kitsune.DropNewest),
		kitsune.WithName("overflow"),
	).ForEach(func(_ context.Context, v int) error {
		time.Sleep(500 * time.Microsecond)
		return nil
	}).Run(ctx, kitsune.WithHook(hook))
	printMetric(reg, "myapp_stage_drops_total")

	// --- Example 4: supervision restarts ---
	fmt.Println("\n=== Supervision restarts ===")
	var attempts int
	_, err = kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			attempts++
			if attempts == 1 {
				return 0, errors.New("transient failure")
			}
			return v * 10, nil
		},
		kitsune.Supervise(kitsune.RestartOnError(1, kitsune.FixedBackoff(0))),
		kitsune.WithName("supervised"),
	).Collect(ctx, kitsune.WithHook(hook))
	if err != nil {
		log.Fatal(err)
	}
	printMetric(reg, "myapp_stage_restarts_total")
}

// printMetric gathers the named metric family and prints all label/value pairs.
func printMetric(reg *prometheus.Registry, name string) {
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := labelsStr(m)
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				fmt.Printf("  %s{%s} = %.0f\n", name, labels, m.GetCounter().GetValue())
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				fmt.Printf("  %s{%s} count=%d sum=%.3fs\n",
					name, labels, h.GetSampleCount(), h.GetSampleSum())
			case dto.MetricType_GAUGE:
				fmt.Printf("  %s{%s} = %.0f\n", name, labels, m.GetGauge().GetValue())
			}
		}
	}
}

func labelsStr(m *dto.Metric) string {
	s := ""
	for i, lp := range m.GetLabel() {
		if i > 0 {
			s += ","
		}
		s += lp.GetName() + "=" + lp.GetValue()
	}
	return s
}
