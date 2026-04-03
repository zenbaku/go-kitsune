// Example: balance — distribute work across N parallel consumers.
//
// Demonstrates: Balance (round-robin fan-out), Broadcast for comparison.
//
// Balance routes each item to exactly one output pipeline in round-robin order.
// Unlike Broadcast (which copies every item to all consumers), Balance divides
// the load so that each consumer processes a disjoint subset of the stream.
// Use Balance for parallel work distribution, sharding, or worker pools.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Scenario 1: distribute 9 tasks across 3 workers ---
	fmt.Println("=== Balance: distribute 9 tasks across 3 workers ===")

	tasks := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9})
	workers := kitsune.Balance(tasks, 3)

	type Result struct {
		Worker int
		Task   int
	}

	runners := make([]*kitsune.Runner, 3)
	results := make([][]Result, 3)

	for i, w := range workers {
		i := i
		runners[i] = w.ForEach(func(_ context.Context, task int) error {
			results[i] = append(results[i], Result{Worker: i + 1, Task: task})
			return nil
		})
	}

	merged, err := kitsune.MergeRunners(runners...)
	if err != nil {
		panic(err)
	}
	if err := merged.Run(context.Background()); err != nil {
		panic(err)
	}

	for i, res := range results {
		tasks := make([]int, len(res))
		for j, r := range res {
			tasks[j] = r.Task
		}
		fmt.Printf("  worker %d: tasks %v\n", i+1, tasks)
	}

	// --- Scenario 2: Balance vs Broadcast ---
	//
	// Balance: each item goes to ONE consumer (load distribution).
	// Broadcast: each item goes to ALL consumers (fan-out copy).
	fmt.Println("\n=== Balance vs Broadcast ===")

	items := kitsune.FromSlice([]string{"a", "b", "c", "d"})
	balOutputs := kitsune.Balance(items, 2)

	var bal0, bal1 []string
	r0 := balOutputs[0].ForEach(func(_ context.Context, s string) error {
		bal0 = append(bal0, s)
		return nil
	})
	r1 := balOutputs[1].ForEach(func(_ context.Context, s string) error {
		bal1 = append(bal1, s)
		return nil
	})
	mr, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		panic(err)
	}
	if err := mr.Run(context.Background()); err != nil {
		panic(err)
	}

	fmt.Printf("  Balance  → output[0]: %v  output[1]: %v\n", bal0, bal1)
	fmt.Printf("  (total items across outputs: %d — same as input)\n", len(bal0)+len(bal1))
}
