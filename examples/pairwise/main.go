// Example: pairwise — emit consecutive overlapping pairs from a stream.
//
// Demonstrates: Pairwise, Map, Collect.
package main

import (
	"context"
	"fmt"
	"math"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Pairwise: consecutive pairs ---
	//
	// Pairwise buffers the previous item and emits a Pair[T,T] for each
	// new item: [1,2,3,4] → [{1,2},{2,3},{3,4}]. The first item is buffered
	// silently; every subsequent item produces one pair.
	fmt.Println("=== Pairwise: consecutive pairs from a slice ===")

	pairs, err := kitsune.Pairwise(kitsune.FromSlice([]int{1, 2, 3, 4, 5})).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for _, p := range pairs {
		fmt.Printf("  {%d, %d}\n", p.First, p.Second)
	}

	// --- Pairwise: compute deltas between readings ---
	//
	// A common use case: compute the change between successive sensor readings.
	fmt.Println("\n=== Pairwise: compute delta between temperature readings ===")

	readings := []float64{36.6, 36.8, 37.1, 37.0, 36.9, 37.2}
	deltas, err := kitsune.Map(
		kitsune.Pairwise(kitsune.FromSlice(readings)),
		func(_ context.Context, p kitsune.Pair[float64, float64]) (float64, error) {
			return math.Round((p.Second-p.First)*10) / 10, nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Readings: %v\n", readings)
	fmt.Printf("Deltas:   %v\n", deltas)

	// --- Pairwise: detect direction changes in a signal ---
	//
	// Pairwise makes it easy to spot when a value changes direction.
	fmt.Println("\n=== Pairwise: detect rising vs falling signal ===")

	signal := []int{5, 7, 9, 8, 6, 7, 10}
	type Change struct {
		From, To  int
		Direction string
	}
	changes, err := kitsune.Map(
		kitsune.Pairwise(kitsune.FromSlice(signal)),
		func(_ context.Context, p kitsune.Pair[int, int]) (Change, error) {
			dir := "flat"
			if p.Second > p.First {
				dir = "rising"
			} else if p.Second < p.First {
				dir = "falling"
			}
			return Change{From: p.First, To: p.Second, Direction: dir}, nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Signal: %v\n", signal)
	for _, c := range changes {
		fmt.Printf("  %2d → %2d  (%s)\n", c.From, c.To, c.Direction)
	}
}
