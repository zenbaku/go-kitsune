// Example: fanout: split a stream and run each branch concurrently.
//
// Demonstrates: Partition, MergeRunners
package main

import (
	"context"
	"fmt"
	"sync"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	nums := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

	// Partition splits the stream into two typed pipelines based on a predicate.
	evens, odds := kitsune.Partition(nums, func(n int) bool { return n%2 == 0 })

	var mu sync.Mutex
	var evenResults, oddResults []int

	// MergeRunners starts both branches from the same shared source and waits
	// for both to finish. All branches must complete before Run returns.
	merged, err := kitsune.MergeRunners(
		evens.ForEach(func(_ context.Context, n int) error {
			mu.Lock()
			evenResults = append(evenResults, n)
			mu.Unlock()
			return nil
		}),
		odds.ForEach(func(_ context.Context, n int) error {
			mu.Lock()
			oddResults = append(oddResults, n)
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		panic(err)
	}
	if _, err := merged.Run(ctx); err != nil {
		panic(err)
	}

	fmt.Println("evens:", evenResults)
	fmt.Println("odds: ", oddResults)
}
