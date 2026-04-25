// Example: broadcast — fan-out a single stream to N independent consumers.
//
// Demonstrates: Broadcast, MergeRunners
package main

import (
	"context"
	"fmt"
	"sync"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	events := kitsune.FromSlice([]string{"login", "purchase", "logout", "search", "purchase"})

	// Broadcast fans the stream out to 3 independent channels. Each consumer
	// sees every item. The source is consumed at the speed of the slowest consumer.
	branches := kitsune.Broadcast(events, 3)

	var mu sync.Mutex
	counts := make([]int, 3)

	runners := make([]kitsune.Runnable, 3)
	for i, branch := range branches {
		i, branch := i, branch
		runners[i] = branch.ForEach(func(_ context.Context, s string) error {
			mu.Lock()
			counts[i]++
			mu.Unlock()
			return nil
		})
	}

	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		panic(err)
	}
	if _, err := merged.Run(ctx); err != nil {
		panic(err)
	}

	for i, c := range counts {
		fmt.Printf("consumer %d received %d items\n", i, c)
	}
}
