// Example: exhaustmap — form submission that ignores clicks while in-flight.
//
// Demonstrates: ExhaustMap dropping upstream items that arrive while the
// current inner pipeline is still active, so only the first trigger wins.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// submitForm simulates a form submission API call.
// It takes time to complete, modelling a network round-trip.
func submitForm(ctx context.Context, submission string, yield func(string) error) error {
	fmt.Printf("  [submitting] %q …\n", submission)
	select {
	case <-time.After(60 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	result := fmt.Sprintf("OK: %q accepted", submission)
	fmt.Printf("  [complete]   %q\n", submission)
	return yield(result)
}

func main() {
	// Simulate a user clicking "Submit" multiple times in quick succession.
	// With ExhaustMap, only the first click is processed; the rest are ignored
	// while the in-flight request is active.
	clicks := kitsune.FromSlice([]string{
		"click-1", // processed
		"click-2", // dropped — click-1 still in flight
		"click-3", // dropped — click-1 still in flight
	})

	fmt.Println("=== ExhaustMap: form submission (first click wins) ===")
	fmt.Println("User clicks Submit three times rapidly:")
	fmt.Println()

	results, err := kitsune.ExhaustMap(clicks, submitForm,
		kitsune.Buffer(8), // buffer clicks so they arrive while inner is active
	).Collect(context.Background())
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Println("Results received by the application:")
	for _, r := range results {
		fmt.Println(" ", r)
	}
	fmt.Printf("\n%d submission(s) completed (duplicate clicks dropped)\n", len(results))

	// Demonstrate sequential submissions (gap between clicks lets each through).
	fmt.Println()
	fmt.Println("=== ExhaustMap: sequential submissions (no overlap) ===")
	sequential := kitsune.FromSlice([]string{"form-A", "form-B"})
	seqResults, err := kitsune.ExhaustMap(sequential, submitForm).Collect(context.Background())
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Println()
	fmt.Println("Results:")
	for _, r := range seqResults {
		fmt.Println(" ", r)
	}
}
