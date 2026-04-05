// Example: broadcast — fan-out a single stream to N independent consumers.
//
// Demonstrates: BroadcastN, MergeRunners
package main

import (
	"context"
	"fmt"
	"sync"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

func main() {
	ctx := context.Background()

	events := kitsune.FromSlice([]string{"login", "purchase", "logout", "search", "purchase"})

	// BroadcastN fans the stream out to 3 independent channels. Each consumer
	// sees every item. The source is consumed at the speed of the slowest consumer.
	branches := kitsune.BroadcastN(events, 3)

	var mu sync.Mutex
	counts := make([]int, 3)

	runners := make([]*kitsune.Runner, 3)
	for i, branch := range branches {
		i, branch := i, branch
		runners[i] = branch.ForEach(func(_ context.Context, s string) error {
			mu.Lock()
			counts[i]++
			mu.Unlock()
			return nil
		}).Build()
	}

	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		panic(err)
	}
	if err := merged.Run(ctx); err != nil {
		panic(err)
	}

	for i, c := range counts {
		fmt.Printf("consumer %d received %d items\n", i, c)
	}
}
