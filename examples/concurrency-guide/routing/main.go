// Example: concurrency-guide/routing: heterogeneous fan-out with Partition.
//
// Events arrive on a single stream. Valid events go to a parallel enrichment
// pipeline; invalid events go to a dead-letter branch. The two branches have
// *different* stage shapes and configurations; that is the signal to reach for
// Partition rather than Concurrency(n).
//
//   - Partition routes by content (predicate on the item), not by load.
//   - The two output pipelines are independent and can each have their own
//     Concurrency, Buffer, OnError, and stage chain.
//   - Both branches must be consumed; MergeRunners starts and waits for both.
//
// Compare with:
//   - Concurrency(n): same stage shape on n goroutines; for load splitting.
//   - Balance(n): round-robin to n identical downstream subgraphs.
//   - Partition: predicate-based split to two *different* downstream subgraphs.
//
// Demonstrates:
//   - Partition(pred) for content-based fan-out
//   - ForEachRunner.Build() and MergeRunners for multi-branch execution
//   - Concurrency on one branch but not the other (heterogeneous shapes)
//
// See: doc/concurrency-guide.md#balance-and-partition-explicit-fan-out
package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Order is an incoming event with a validity flag.
type Order struct {
	ID    int
	Value int
	Valid bool
}

func main() {
	ctx := context.Background()

	orders := []Order{
		{1, 100, true}, {2, -5, false}, {3, 250, true},
		{4, 0, false}, {5, 75, true}, {6, 300, true},
		{7, -10, false}, {8, 500, true}, {9, 0, false},
		{10, 150, true},
	}

	src := kitsune.FromSlice(orders)

	// Partition splits into (valid, invalid). The predicate is evaluated once
	// per item; each item goes to exactly one branch.
	valid, invalid := kitsune.Partition(src, func(o Order) bool { return o.Valid })

	var mu sync.Mutex
	var processed, deadLettered []int

	// Valid branch: enrich with a simulated API call, then store.
	enriched := kitsune.Map(valid,
		func(_ context.Context, o Order) (Order, error) {
			time.Sleep(5 * time.Millisecond) // simulate enrichment latency
			o.Value = o.Value * 110 / 100    // apply 10% uplift
			return o, nil
		},
		kitsune.Concurrency(4), // parallel enrichment
		kitsune.Buffer(16),     // absorb bursts from the splitter
		kitsune.WithName("enrich"),
	)
	// MergeRunners starts both branches from the shared source and waits for
	// both to finish. Forgetting to consume one branch stalls the other via
	// backpressure; MergeRunners prevents that mistake.
	merged, err := kitsune.MergeRunners(
		enriched.ForEach(func(_ context.Context, o Order) error {
			mu.Lock()
			processed = append(processed, o.ID)
			mu.Unlock()
			return nil
		}, kitsune.WithName("store")),
		invalid.ForEach(func(_ context.Context, o Order) error {
			mu.Lock()
			deadLettered = append(deadLettered, o.ID)
			mu.Unlock()
			return nil
		}, kitsune.WithName("dead-letter")),
	)
	if err != nil {
		panic(err)
	}
	if _, err := merged.Run(ctx); err != nil {
		panic(err)
	}

	sort.Ints(processed)
	sort.Ints(deadLettered)

	fmt.Println("=== Partition routing ===")
	fmt.Printf("  processed (valid):    IDs %v\n", processed)
	fmt.Printf("  dead-lettered (invalid): IDs %v\n", deadLettered)
	fmt.Printf("  total: %d processed + %d rejected = %d\n",
		len(processed), len(deadLettered), len(orders))
}
