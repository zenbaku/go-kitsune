// Example: ticker — scheduled sources that emit on a regular interval.
//
// Demonstrates: Ticker, Interval, Take, Map, Collect.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Ticker: emit time.Time on every tick ---
	//
	// Ticker wraps time.NewTicker as a pipeline source. Use Take to bound
	// an otherwise infinite stream. Useful for health-check loops, polling,
	// or time-triggered batch flushes.
	fmt.Println("=== Ticker: emit timestamps every 50ms, collect 4 ===")

	times, err := kitsune.Ticker(50 * time.Millisecond).
		Take(4).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for i, t := range times {
		if i == 0 {
			fmt.Printf("  tick 0: %s (base)\n", t.Format("15:04:05.000"))
			continue
		}
		fmt.Printf("  tick %d: +%dms\n", i, t.Sub(times[0]).Milliseconds())
	}

	// --- Interval: emit a counter (0, 1, 2, …) on every tick ---
	//
	// Interval is like Ticker but emits an int64 sequence starting at 0.
	// Convenient when you need the tick number rather than the wall-clock time.
	fmt.Println("\n=== Interval: emit 0,1,2,3,4 at 30ms intervals ===")

	counts, err := kitsune.Interval(30 * time.Millisecond).
		Take(5).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Println("Counts:", counts)

	// --- Ticker + Map: scheduled work ---
	//
	// Combine Ticker with Map to run a function on a regular schedule.
	// Here we "ping" a service every 60ms and collect 3 responses.
	fmt.Println("\n=== Ticker + Map: periodic health check ===")

	type HealthResult struct {
		At     time.Time
		Status string
	}

	checks, err := kitsune.Map(
		kitsune.Ticker(60*time.Millisecond).Take(3),
		func(_ context.Context, t time.Time) (HealthResult, error) {
			// Simulate a fast health-check call.
			return HealthResult{At: t, Status: "ok"}, nil
		},
		kitsune.WithName("health-check"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Checks run: %d\n", len(checks))
	for _, c := range checks {
		fmt.Printf("  %s → %s\n", c.At.Format("15:04:05.000"), c.Status)
	}
}
