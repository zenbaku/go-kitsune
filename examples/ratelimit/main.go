// Example: ratelimit — token-bucket throughput control.
//
// Demonstrates: RateLimit, Burst, RateMode, RateLimitWait, RateLimitDrop.
//
// RateLimit is different from Throttle: Throttle allows exactly one item per
// fixed window; RateLimit uses a token bucket that supports configurable burst
// and two modes — Wait (backpressure, default) and Drop (discard excess).
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Wait mode: backpressure at 10 items/sec ---
	//
	// 6 items with a rate of 10/sec and burst of 1. The first item passes
	// immediately (uses the initial token); each subsequent item must wait
	// ~100ms for the bucket to refill. The pipeline produces all 6 items —
	// nothing is dropped — but upstream is naturally slowed down.
	fmt.Println("=== RateLimit Wait mode: 10 items/sec, burst=1 ===")

	start := time.Now()
	results, err := kitsune.RateLimit(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		10, // 10 items per second
		kitsune.Burst(1),
		kitsune.WithName("rate-wait"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	elapsed := time.Since(start)

	fmt.Printf("Received all %d items in %v\n", len(results), elapsed.Round(50*time.Millisecond))
	fmt.Println("Items:", results)

	// --- Wait mode with burst: initial burst passes immediately ---
	//
	// With burst=5 the token bucket starts full: the first 5 items are
	// released instantly, and only the 6th item must wait for a new token.
	// Burst is useful for absorbing bursty traffic without unnecessary delay.
	fmt.Println("\n=== RateLimit Wait mode: 10 items/sec, burst=5 ===")

	start = time.Now()
	burstResults, err := kitsune.RateLimit(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
		10,
		kitsune.Burst(5),
		kitsune.WithName("rate-burst"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	burstElapsed := time.Since(start)

	fmt.Printf("With burst=5: received %d items in %v (vs %v without burst)\n",
		len(burstResults), burstElapsed.Round(50*time.Millisecond), elapsed.Round(50*time.Millisecond))

	// --- Drop mode: excess items are discarded, upstream runs at full speed ---
	//
	// Drop mode never blocks — when the token bucket is empty the item is
	// silently discarded (counted as skipped in the hook). Use this when you
	// want to limit downstream load without slowing the producer.
	//
	// With rate=10/sec and burst=1, the bucket starts with 1 token. Since the
	// pipeline runs in microseconds (far less than 1 second), the bucket never
	// refills: only the very first item passes.
	fmt.Println("\n=== RateLimit Drop mode: 10 items/sec, burst=1 ===")

	var produced atomic.Int64
	source := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
		func(_ context.Context, n int) (int, error) {
			produced.Add(1)
			return n, nil
		},
		kitsune.WithName("producer"),
	)

	m := kitsune.NewMetricsHook()
	dropResults, err := kitsune.RateLimit(
		source, 10,
		kitsune.Burst(1),
		kitsune.RateMode(kitsune.RateLimitDrop),
		kitsune.WithName("rate-drop"),
	).Collect(context.Background(), kitsune.WithHook(m))
	if err != nil {
		panic(err)
	}

	skip := m.Stage("rate-drop").Skipped
	fmt.Printf("Produced %d items, passed %d, dropped %d\n",
		produced.Load(), len(dropResults), skip)
	if len(dropResults) > 0 {
		fmt.Println("Passed item:", dropResults[0])
	}
}
