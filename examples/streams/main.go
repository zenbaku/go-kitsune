// Example: streams — infinite and generative sources (Unfold, Iterate, Repeatedly, Cycle, Concat).
//
// Demonstrates: Unfold (seed-based generation), Iterate (successive function application),
// Repeatedly (call-based generation), Cycle (infinite looping), Concat (sequential pipelines).
package main

import (
	"context"
	"fmt"
	"math/rand/v2"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Unfold: generate from a seed by repeatedly applying a function.
	// The function returns (value, nextSeed, stop). Classic for sequences.
	// -------------------------------------------------------------------------
	fmt.Println("=== Unfold: Fibonacci sequence ===")
	fibs, _ := kitsune.Unfold([2]int{0, 1}, func(s [2]int) (int, [2]int, bool) {
		return s[0], [2]int{s[1], s[0] + s[1]}, false
	}).Take(10).Collect(ctx)
	fmt.Println("Fibonacci:", fibs)

	// Unfold can also signal halt to produce a finite stream.
	fmt.Println("\n=== Unfold: countdown ===")
	countdown, _ := kitsune.Unfold(5, func(n int) (int, int, bool) {
		if n < 0 {
			return 0, 0, true // halt
		}
		return n, n - 1, false
	}).Collect(ctx)
	fmt.Println("Countdown:", countdown)

	// -------------------------------------------------------------------------
	// Iterate: start with a seed, apply fn to produce the next value.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Iterate: powers of 2 ===")
	powers, _ := kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(8).Collect(ctx)
	fmt.Println("Powers of 2:", powers)

	// -------------------------------------------------------------------------
	// Repeatedly: call fn on each iteration. Good for sampling, random values,
	// or any stateful/effectful generator.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Repeatedly: 5 random numbers (seeded) ===")
	rng := rand.New(rand.NewPCG(42, 0))
	randoms, _ := kitsune.Repeatedly(func() int { return rng.IntN(100) }).Take(5).Collect(ctx)
	fmt.Println("Randoms:", randoms)

	// -------------------------------------------------------------------------
	// Cycle: loop over a fixed slice forever.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Cycle: round-robin over 3 workers ===")
	workers, _ := kitsune.Cycle([]string{"worker-1", "worker-2", "worker-3"}).Take(7).Collect(ctx)
	fmt.Println("Workers:", workers)

	// -------------------------------------------------------------------------
	// Concat: run multiple pipeline factories sequentially.
	// Each factory produces its own graph; Concat stitches them in order.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Concat: three page results ===")
	page := func(items ...string) func() *kitsune.Pipeline[string] {
		return func() *kitsune.Pipeline[string] { return kitsune.FromSlice(items) }
	}
	all, _ := kitsune.Concat(
		page("item-1", "item-2"),
		page("item-3", "item-4", "item-5"),
		page("item-6"),
	).Collect(ctx)
	fmt.Println("Concatenated:", all)
}
