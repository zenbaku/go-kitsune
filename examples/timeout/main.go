// Example: timeout — enforce per-item deadlines on slow stages.
//
// Demonstrates: Timeout StageOption, OnError(Skip()), OnError(Return(...))
//
// Timeout passes a deadline-scoped context to the stage function.
// The function must respect ctx.Done() to be interrupted by the deadline.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	delays := []time.Duration{
		5 * time.Millisecond,
		50 * time.Millisecond,
		5 * time.Millisecond,
		80 * time.Millisecond,
		5 * time.Millisecond,
		30 * time.Millisecond,
		5 * time.Millisecond,
	}

	// The context passed to each fn has a per-item deadline.
	// The fn must select on ctx.Done() so it can be interrupted.
	slow := func(ctx context.Context, d time.Duration) (string, error) {
		select {
		case <-time.After(d):
			return fmt.Sprintf("done in %v", d), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	// --- Skip timed-out items ---

	fmt.Println("=== Timeout + Skip ===")
	results, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(delays), slow,
			kitsune.Timeout(20*time.Millisecond),
			kitsune.OnError(kitsune.Skip()),
			kitsune.WithName("slow-op"),
		))
	if err != nil {
		panic(err)
	}
	fmt.Printf("received %d of %d items (timed-out items skipped)\n", len(results), len(delays))
	for _, r := range results {
		fmt.Println(" ", r)
	}

	// --- Return a default value on timeout ---

	fmt.Println("\n=== Timeout + Return default ===")
	results2, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(delays), slow,
			kitsune.Timeout(20*time.Millisecond),
			kitsune.OnError(kitsune.Return("timed out")),
			kitsune.WithName("slow-op-default"),
		))
	if err != nil {
		panic(err)
	}
	fmt.Printf("received %d items (timeouts replaced with default)\n", len(results2))
	for _, r := range results2 {
		fmt.Println(" ", r)
	}
}
