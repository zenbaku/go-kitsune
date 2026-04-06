// Example: timeout — per-item deadline on Map and FlatMap stages.
//
// Demonstrates: Timeout StageOption, OnError(Skip), Map, Collect.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// slowLookup simulates an external API call that may take a variable amount of time.
// It respects context cancellation so that Timeout can interrupt it.
func slowLookup(ctx context.Context, id int) (string, error) {
	delay := time.Duration(id*30) * time.Millisecond // id=1→30ms, id=2→60ms, ...
	select {
	case <-time.After(delay):
		return fmt.Sprintf("result-%d", id), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func main() {
	// --- Timeout: fast items pass through, slow items are cancelled ---
	//
	// A 100ms deadline is applied per item. IDs 1–3 finish in time (30–90ms);
	// IDs 4–5 exceed the deadline (120–150ms) and are skipped.
	fmt.Println("=== Timeout: cancel slow API calls, skip timed-out items ===")

	ids := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	results, err := kitsune.Map(
		ids,
		slowLookup,
		kitsune.Timeout(100*time.Millisecond),
		kitsune.OnError(kitsune.Skip()),
		kitsune.WithName("lookup"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Items: 5  Results: %d  Timed out: %d\n", len(results), 5-len(results))
	for _, r := range results {
		fmt.Println(" ", r)
	}

	// --- Timeout with retry ---
	//
	// Combine Timeout with Retry so a timed-out item gets another attempt
	// before being dropped. Each attempt gets a fresh deadline.
	fmt.Println("\n=== Timeout + Retry: each attempt gets a fresh deadline ===")
	const budget = 50 * time.Millisecond

	attempts := kitsune.FromSlice([]int{1, 2}) // id=1→30ms (passes), id=2→60ms (times out on all retries)
	results2, err := kitsune.Map(
		attempts,
		slowLookup,
		kitsune.Timeout(budget),
		kitsune.OnError(kitsune.RetryThen(2, kitsune.FixedBackoff(0), kitsune.Skip())),
		kitsune.WithName("lookup-retry"),
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Submitted: 2  Passed: %d  Dropped after retries: %d\n",
		len(results2), 2-len(results2))
	for _, r := range results2 {
		fmt.Println(" ", r)
	}
}
