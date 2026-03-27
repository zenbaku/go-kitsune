// Example: broadcast — send every item to multiple consumers.
//
// Demonstrates: Broadcast (fan-out to all), Merge, MergeRunners.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	orders := kitsune.FromSlice([]int{100, 200, 300})

	// Broadcast every order to 3 independent consumers.
	copies := kitsune.Broadcast(orders, 3)

	// Each consumer does different work on the same data.
	analytics := kitsune.Map(copies[0], func(_ context.Context, n int) (string, error) {
		return fmt.Sprintf("analytics: order $%d", n), nil
	})
	billing := kitsune.Map(copies[1], func(_ context.Context, n int) (string, error) {
		return fmt.Sprintf("billing: charge $%d", n), nil
	})
	audit := kitsune.Map(copies[2], func(_ context.Context, n int) (string, error) {
		return fmt.Sprintf("audit: logged $%d", n), nil
	})

	// Run all three branches.
	r1 := analytics.ForEach(func(_ context.Context, s string) error { fmt.Println(" ", s); return nil })
	r2 := billing.ForEach(func(_ context.Context, s string) error { fmt.Println(" ", s); return nil })
	r3 := audit.ForEach(func(_ context.Context, s string) error { fmt.Println(" ", s); return nil })

	err := kitsune.MergeRunners(r1, r2, r3).Run(context.Background())
	if err != nil {
		panic(err)
	}

	// --- Broadcast + Merge: recombine after independent transforms ---
	fmt.Println("\n=== Broadcast → transform → Merge ===")
	nums := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(nums, 2)

	doubled := kitsune.Map(branches[0], func(_ context.Context, n int) (int, error) { return n * 2, nil })
	tripled := kitsune.Map(branches[1], func(_ context.Context, n int) (int, error) { return n * 3, nil })

	merged := kitsune.Merge(doubled, tripled)
	results, err := merged.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("All results:", results)
	// Contains [2,4,6] and [3,6,9] interleaved (order not guaranteed).
}
