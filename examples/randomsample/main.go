// Example: randomsample: streaming probabilistic filter.
//
// Demonstrates: RandomSample.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Use a seeded source for a reproducible demo.
	_ = rand.New(rand.NewSource(42))

	// rate=0: nothing passes.
	none, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 0.0),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rate=0.0: %v (len=%d)\n", none, len(none))

	// rate=1: everything passes.
	all, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 1.0),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rate=1.0: %v\n", all)

	// rate=0.1: roughly 10 percent of 1000 items pass through.
	input := make([]int, 1000)
	for i := range input {
		input[i] = i
	}
	sampled, err := kitsune.Collect(ctx,
		kitsune.RandomSample(kitsune.FromSlice(input), 0.1),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rate=0.1 of 1000 items: sampled %d items\n", len(sampled))
}
