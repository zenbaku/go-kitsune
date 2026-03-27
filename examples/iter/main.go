// Example: iter — creating pipelines from Go iterators.
//
// Demonstrates: FromIter with slices.Values, maps.Keys, and custom iterators.
package main

import (
	"context"
	"fmt"
	"maps"
	"slices"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- From a slice iterator ---
	fmt.Println("=== slices.Values ===")
	items := []string{"alpha", "bravo", "charlie"}
	p := kitsune.FromIter(slices.Values(items))
	results, _ := p.Collect(context.Background())
	fmt.Println(results)

	// --- From map keys ---
	fmt.Println("\n=== maps.Keys ===")
	scores := map[string]int{"alice": 95, "bob": 82, "carol": 91}
	names := kitsune.FromIter(maps.Keys(scores))
	sorted, _ := names.Collect(context.Background())
	slices.Sort(sorted)
	fmt.Println(sorted)

	// --- Custom infinite iterator + Take ---
	fmt.Println("\n=== Fibonacci (take 10) ===")
	fib := func(yield func(int) bool) {
		a, b := 0, 1
		for {
			if !yield(a) {
				return
			}
			a, b = b, a+b
		}
	}
	fibs, _ := kitsune.FromIter(fib).Take(10).Collect(context.Background())
	fmt.Println(fibs)

	// --- Drain: consume without collecting ---
	fmt.Println("\n=== Drain (side effects only) ===")
	kitsune.FromIter(slices.Values([]int{1, 2, 3})).
		Tap(func(n int) { fmt.Printf("  processing %d\n", n) }).
		Drain().
		Run(context.Background())
}
