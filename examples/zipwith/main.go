// Example: zipwith — combine two pipeline branches into a custom type in one step.
//
// Demonstrates: ZipWith, Broadcast, combining unlike types without an intermediate Pair.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

type Product struct {
	Name  string
	Price float64
}

type PricedProduct struct {
	Name          string
	OriginalPrice float64
	SalePrice     float64
}

func main() {
	ctx := context.Background()

	// --- Basic: combine integers from two branches into a single value ---
	fmt.Println("=== Sum paired branches ===")
	branches := kitsune.Broadcast[int](kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 2)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	results, err := kitsune.ZipWith(branches[0], doubled,
		func(_ context.Context, original, d int) (int, error) {
			return original + d, nil
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("Results:", results) // [3 6 9 12 15]

	// --- Different types: struct + computed value → combined struct ---
	fmt.Println("\n=== Apply 20% discount ===")
	products := []Product{
		{"Widget", 10.00},
		{"Gadget", 25.00},
		{"Doohickey", 8.50},
	}
	pb := kitsune.Broadcast[Product](kitsune.FromSlice(products), 2)
	salePrices := kitsune.Map(pb[1], func(_ context.Context, p Product) (float64, error) {
		return p.Price * 0.8, nil
	})
	priced, err := kitsune.ZipWith(pb[0], salePrices,
		func(_ context.Context, p Product, sale float64) (PricedProduct, error) {
			return PricedProduct{
				Name:          p.Name,
				OriginalPrice: p.Price,
				SalePrice:     sale,
			}, nil
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	for _, pp := range priced {
		fmt.Printf("  %-12s $%.2f  →  $%.2f\n", pp.Name, pp.OriginalPrice, pp.SalePrice)
	}

	// --- ZipWith stops when the shorter stream closes, same as Zip ---
	fmt.Println("\n=== Stops at shorter stream ===")
	src := kitsune.FromSlice([]int{10, 20, 30, 40, 50})
	b2 := kitsune.Broadcast[int](src, 2)
	short := b2[0].Take(3)
	full := b2[1]
	limited, err := kitsune.ZipWith(short, full,
		func(_ context.Context, a, b int) (string, error) {
			return fmt.Sprintf("(%d,%d)", a, b), nil
		},
	).Collect(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("  Emitted %d pairs: %v\n", len(limited), limited)
}
