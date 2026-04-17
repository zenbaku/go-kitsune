// Example: within: apply pipeline operators to slice contents.
//
// Demonstrates: Within, Batch, BatchCount, Sort, Filter, Unbatch.
package main

import (
	"context"
	"fmt"
	"log"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Scenario 1: sort each chunk, then flatten back to a stream.
	src := kitsune.FromSlice([]int{3, 1, 2, 6, 4, 5, 9, 7, 8})
	sorted, err := kitsune.Collect(ctx,
		kitsune.Unbatch(
			kitsune.Within(
				kitsune.Batch(src, kitsune.BatchCount(3)),
				func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
					return kitsune.Sort(w, func(a, b int) bool { return a < b })
				},
			),
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sorted per chunk: %v\n", sorted)

	// Scenario 2: filter each chunk to keep only the higher values.
	nums := kitsune.FromSlice([]int{10, 3, 7, 2, 9, 4, 8, 1, 6})
	peaks, err := kitsune.Collect(ctx,
		kitsune.Within(
			kitsune.Batch(nums, kitsune.BatchCount(3)),
			func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
				return kitsune.Filter(w, func(_ context.Context, v int) (bool, error) {
					return v >= 7, nil
				})
			},
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	for i, chunk := range peaks {
		fmt.Printf("chunk %d peaks: %v\n", i, chunk)
	}
}
