// Example: reducewhile: fold a stream, stopping early when a predicate triggers.
//
// Demonstrates: ReduceWhile, Single.
package main

import (
	"context"
	"fmt"
	"log"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Sum prices until the running total hits 100.
	prices := kitsune.FromSlice([]float64{10, 20, 30, 40, 50, 60})
	total, err := kitsune.Single(ctx,
		kitsune.ReduceWhile(prices, 0.0,
			func(acc, p float64) (float64, bool) {
				next := acc + p
				return next, next < 100.0
			},
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stopped at total = %.0f\n", total)

	// Without an early stop, ReduceWhile folds the entire stream.
	allPrices := kitsune.FromSlice([]float64{1, 2, 3, 4, 5})
	sum, err := kitsune.Single(ctx,
		kitsune.ReduceWhile(allPrices, 0.0,
			func(acc, p float64) (float64, bool) { return acc + p, true },
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("full sum = %.0f\n", sum)
}
