// Example: concurrent — parallel processing with multiple workers.
//
// Demonstrates: Concurrency option, Buffer option, WithName
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// Simulate 20 items that each take 50ms to process.
	// With Concurrency(5), total time ≈ 200ms instead of 1000ms.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	start := time.Now()
	input := kitsune.FromSlice(items)
	processed := kitsune.Map(input, func(_ context.Context, n int) (string, error) {
		time.Sleep(50 * time.Millisecond) // simulate work
		return fmt.Sprintf("item-%d", n), nil
	},
		kitsune.Concurrency(5),
		kitsune.WithName("slow-transform"),
	)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	results, err := processed.Collect(
		context.Background(),
		kitsune.WithHook(kitsune.LogHook(logger)),
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("\nProcessed %d items in %v (5 workers × 50ms each)\n", len(results), time.Since(start).Round(time.Millisecond))
	// Note: with Concurrency > 1, output order is NOT guaranteed.
}
