// Example: batch — grouping items into fixed-size slices.
//
// Demonstrates: Batch, Unbatch, BatchTimeout
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Fixed-size batching ---
	items := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7})
	batched := kitsune.Batch(items, 3)

	batches, err := batched.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for i, b := range batches {
		fmt.Printf("Batch %d: %v\n", i, b)
	}

	// --- Batch → process → Unbatch ---
	fmt.Println("\nBatch, double each batch sum, unbatch:")
	items2 := kitsune.FromSlice([]int{10, 20, 30, 40, 50})
	batched2 := kitsune.Batch(items2, 2)
	processed := kitsune.Map(batched2, func(_ context.Context, batch []int) ([]int, error) {
		out := make([]int, len(batch))
		for i, v := range batch {
			out[i] = v * 2
		}
		return out, nil
	})
	flat := kitsune.Unbatch(processed)
	results, err := flat.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Results:", results)

	// --- Batch with timeout (flushes partial batches) ---
	fmt.Println("\nBatch with 100ms timeout:")
	slow := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		for _, s := range []string{"a", "b", "c"} {
			if !yield(s) {
				return nil
			}
		}
		// Pause longer than the timeout — forces a partial flush.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		for _, s := range []string{"d", "e"} {
			if !yield(s) {
				return nil
			}
		}
		return nil
	})
	timedBatches, err := kitsune.Batch(slow, 10, kitsune.BatchTimeout(100*time.Millisecond)).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for i, b := range timedBatches {
		fmt.Printf("  Batch %d: %v\n", i, b)
	}
}
