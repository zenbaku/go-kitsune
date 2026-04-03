// Example: aggregate — terminal aggregations (Sum, Min, Max, MinMax, MinBy, MaxBy,
// Find, Frequencies, FrequenciesBy, ReduceWhile, TakeRandom) plus streaming
// aggregation with CountBy and SumBy.
//
// Demonstrates: numeric aggregates, keyed min/max, frequency counting,
// early-termination fold, random sampling, and streaming count/sum by key.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

type Product struct {
	Name     string
	Category string
	Price    float64
}

var catalog = []Product{
	{"Laptop", "electronics", 999.99},
	{"Mouse", "electronics", 29.99},
	{"Desk", "furniture", 249.99},
	{"Chair", "furniture", 189.99},
	{"Monitor", "electronics", 349.99},
	{"Keyboard", "electronics", 79.99},
	{"Lamp", "furniture", 49.99},
}

func main() {
	ctx := context.Background()

	prices := kitsune.Map(
		kitsune.FromSlice(catalog),
		func(_ context.Context, p Product) (float64, error) { return p.Price, nil },
	)

	// -------------------------------------------------------------------------
	// Sum: total of all prices.
	// -------------------------------------------------------------------------
	total, _ := kitsune.Sum(ctx, prices)
	fmt.Printf("Total inventory value: $%.2f\n", total)

	// -------------------------------------------------------------------------
	// Min / Max / MinMax: cheapest, most expensive, both in one pass.
	// -------------------------------------------------------------------------
	less := func(a, b float64) bool { return a < b }

	prices2 := kitsune.Map(kitsune.FromSlice(catalog), func(_ context.Context, p Product) (float64, error) { return p.Price, nil })
	prices3 := kitsune.Map(kitsune.FromSlice(catalog), func(_ context.Context, p Product) (float64, error) { return p.Price, nil })
	prices4 := kitsune.Map(kitsune.FromSlice(catalog), func(_ context.Context, p Product) (float64, error) { return p.Price, nil })

	min, _, _ := kitsune.Min(ctx, prices2, less)
	max, _, _ := kitsune.Max(ctx, prices3, less)
	rng, _, _ := kitsune.MinMax(ctx, prices4, less)

	fmt.Printf("Cheapest: $%.2f   Most expensive: $%.2f   Range: $%.2f–$%.2f\n",
		min, max, rng.First, rng.Second)

	// -------------------------------------------------------------------------
	// MinBy / MaxBy: find the actual product with the lowest/highest price.
	// -------------------------------------------------------------------------
	byPrice := func(a, b float64) bool { return a < b }

	cheapest, _, _ := kitsune.MinBy(ctx, kitsune.FromSlice(catalog),
		func(p Product) float64 { return p.Price }, byPrice)
	priciest, _, _ := kitsune.MaxBy(ctx, kitsune.FromSlice(catalog),
		func(p Product) float64 { return p.Price }, byPrice)

	fmt.Printf("Cheapest product: %s ($%.2f)\n", cheapest.Name, cheapest.Price)
	fmt.Printf("Priciest product: %s ($%.2f)\n", priciest.Name, priciest.Price)

	// -------------------------------------------------------------------------
	// Find: first product matching a predicate. Stops the pipeline early.
	// -------------------------------------------------------------------------
	affordable, ok, _ := kitsune.Find(ctx, kitsune.FromSlice(catalog),
		func(p Product) bool { return p.Price < 50.0 })
	if ok {
		fmt.Printf("First under $50: %s ($%.2f)\n", affordable.Name, affordable.Price)
	}

	// -------------------------------------------------------------------------
	// Frequencies / FrequenciesBy: count occurrences.
	// -------------------------------------------------------------------------
	tags := kitsune.FromSlice([]string{"go", "python", "go", "rust", "python", "go", "typescript"})
	freq, _ := kitsune.Frequencies(ctx, tags)
	fmt.Printf("\nLanguage frequencies: go=%d python=%d rust=%d typescript=%d\n",
		freq["go"], freq["python"], freq["rust"], freq["typescript"])

	catFreq, _ := kitsune.FrequenciesBy(ctx, kitsune.FromSlice(catalog),
		func(p Product) string { return p.Category })
	fmt.Printf("Products per category: electronics=%d furniture=%d\n",
		catFreq["electronics"], catFreq["furniture"])

	// -------------------------------------------------------------------------
	// ReduceWhile: fold until a condition is met.
	// Budget shopping: add items until we hit $500.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Budget shopping: stop at $500 ===")
	budget := 500.0
	spent, _ := kitsune.ReduceWhile(ctx, kitsune.FromSlice(catalog), 0.0,
		func(acc float64, p Product) (float64, bool) {
			next := acc + p.Price
			fmt.Printf("  Adding %s ($%.2f) → total $%.2f\n", p.Name, p.Price, next)
			return next, next < budget
		})
	fmt.Printf("Stopped at $%.2f (budget: $%.2f)\n", spent, budget)

	// -------------------------------------------------------------------------
	// TakeRandom: random sample without replacement.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Random sample: 3 featured products ===")
	featured, _ := kitsune.TakeRandom(ctx, kitsune.FromSlice(catalog), 3)
	for _, p := range featured {
		fmt.Printf("  • %s ($%.2f)\n", p.Name, p.Price)
	}

	// -------------------------------------------------------------------------
	// CountBy: streaming count per category (emits snapshot after each item).
	// -------------------------------------------------------------------------
	fmt.Println("\n=== CountBy: product count per category ===")
	countStream := kitsune.CountBy(
		kitsune.FromSlice(catalog),
		func(p Product) string { return p.Category },
	)
	snapshots, _ := countStream.Collect(ctx)
	last := snapshots[len(snapshots)-1]
	fmt.Printf("electronics=%d furniture=%d\n", last["electronics"], last["furniture"])

	// -------------------------------------------------------------------------
	// SumBy: running revenue total per category.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== SumBy: revenue per category ===")
	sumStream := kitsune.SumBy(
		kitsune.FromSlice(catalog),
		func(p Product) string { return p.Category },
		func(p Product) float64 { return p.Price },
	)
	sumSnaps, _ := sumStream.Collect(ctx)
	lastSum := sumSnaps[len(sumSnaps)-1]
	fmt.Printf("electronics=$%.2f furniture=$%.2f\n", lastSum["electronics"], lastSum["furniture"])
}
