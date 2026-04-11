// Example: ticker — time-based sources and limiting.
//
// Demonstrates: Ticker, Timer, Take, Map
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
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

	// --- Ticker as counter: map time.Time to a monotonic index ---

	fmt.Println("\n=== Ticker as counter (4 ticks) ===")
	var i int64
	counts, err := kitsune.Collect(ctx,
		kitsune.Map(
			kitsune.Take(kitsune.Ticker(20*time.Millisecond), 4),
			func(_ context.Context, _ time.Time) (int64, error) {
				n := i
				i++
				return n, nil
			}))
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
