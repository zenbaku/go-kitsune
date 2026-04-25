// Example: ratelimit: throttle pipeline throughput with a token bucket.
//
// Demonstrates: RateLimit, RateLimitWait (backpressure), RateLimitDrop, Burst
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	// --- RateLimitWait: backpressure; blocks until a token is available ---
	//
	// The pipeline slows to 50 items/sec. Source and all upstream stages apply
	// backpressure automatically because channels fill up.

	fmt.Println("=== RateLimitWait (backpressure) ===")
	start := time.Now()
	results, err := kitsune.Collect(ctx,
		kitsune.RateLimit(kitsune.FromSlice(items[:10]), 200,
			[]kitsune.RateLimitOpt{kitsune.Burst(5)},
		))
	if err != nil {
		panic(err)
	}
	fmt.Printf("  processed %d items in %v\n\n", len(results), time.Since(start).Round(time.Millisecond))

	// --- RateLimitDrop: excess items are silently discarded ---
	//
	// Useful for metrics, sampling, or best-effort delivery where dropping
	// occasional items is acceptable.

	fmt.Println("=== RateLimitDrop (drop excess) ===")
	fast := kitsune.FromSlice(items) // 20 items arrive instantly
	limited := kitsune.RateLimit(fast, 5,
		[]kitsune.RateLimitOpt{kitsune.RateMode(kitsune.RateLimitDrop), kitsune.Burst(3)},
	)
	dropped, err := kitsune.Collect(ctx, limited)
	if err != nil {
		panic(err)
	}
	fmt.Printf("  received %d of %d items (%d dropped)\n",
		len(dropped), len(items), len(items)-len(dropped))
}
