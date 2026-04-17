// Example: single: collect a one-item pipeline with Single.
//
// Demonstrates: Single, OrDefault, OrZero, Reduce, GroupBy.
package main

import (
	"context"
	"fmt"
	"log"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Reduce produces one emission: the final sum.
	sum, err := kitsune.Single(ctx,
		kitsune.Reduce(kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 0, func(acc, v int) int {
			return acc + v
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sum = %d\n", sum)

	// OrDefault: fallback value for empty pipelines.
	def, err := kitsune.Single(ctx, kitsune.Empty[string](), kitsune.OrDefault("fallback"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("default = %q\n", def)

	// OrZero: zero value for empty pipelines.
	zero, err := kitsune.Single(ctx, kitsune.Empty[int](), kitsune.OrZero[int]())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("zero = %d\n", zero)

	// GroupBy naturally emits a single map on close; Single unwraps it.
	groups, err := kitsune.Single(ctx,
		kitsune.GroupBy(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6}),
			func(v int) string {
				if v%2 == 0 {
					return "even"
				}
				return "odd"
			},
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("even=%v odd=%v\n", groups["even"], groups["odd"])
}
