// Example: ticker — time-based sources and limiting.
//
// Demonstrates: Ticker, Interval, Timer, Take, Map
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

func main() {
	ctx := context.Background()

	// --- Ticker: emits time.Time at each tick, take first 5 ---

	fmt.Println("=== Ticker (5 ticks) ===")
	ticks, err := kitsune.Collect(ctx,
		kitsune.Map(
			kitsune.Take(kitsune.Ticker(20*time.Millisecond), 5),
			func(_ context.Context, t time.Time) (string, error) {
				return t.Format("15:04:05.000"), nil
			}))
	if err != nil {
		panic(err)
	}
	for _, ts := range ticks {
		fmt.Println(" ", ts)
	}

	// --- Interval: emits a monotonically increasing counter ---

	fmt.Println("\n=== Interval (counter, 4 ticks) ===")
	counts, err := kitsune.Collect(ctx, kitsune.Take(kitsune.Interval(20*time.Millisecond), 4))
	if err != nil {
		panic(err)
	}
	fmt.Println(" ", counts)

	// --- Timer: emits one value after a delay ---

	fmt.Println("\n=== Timer (fires once after 20ms) ===")
	msg, err := kitsune.Collect(ctx,
		kitsune.Timer(20*time.Millisecond, func() string { return "fired!" }))
	if err != nil {
		panic(err)
	}
	fmt.Println(" ", msg)
}
