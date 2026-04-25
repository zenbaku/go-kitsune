// Example: basic: a minimal linear pipeline.
//
// Demonstrates: FromSlice, Map, Filter, ForEach, Collect
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// --- ForEach: emit lines as we go ---

	words := []string{"  hello  ", "  world  ", "  kitsune  ", "  go  "}

	trimmed := kitsune.Map(kitsune.FromSlice(words),
		func(_ context.Context, s string) (string, error) {
			return strings.TrimSpace(s), nil
		})

	long := kitsune.Filter(trimmed,
		func(_ context.Context, s string) (bool, error) {
			return len(s) > 3, nil
		})

	upper := kitsune.Map(long,
		func(_ context.Context, s string) (string, error) {
			return strings.ToUpper(s), nil
		})

	_, err := upper.ForEach(func(_ context.Context, s string) error {
		fmt.Println(s)
		return nil
	}).Run(ctx)
	if err != nil {
		panic(err)
	}

	// --- Collect: materialise results into a slice ---

	nums := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8}),
		func(_ context.Context, n int) (int, error) { return n * n, nil })

	evens := kitsune.Filter(nums,
		func(_ context.Context, n int) (bool, error) { return n%2 == 0, nil })

	squares, err := kitsune.Collect(ctx, evens)
	if err != nil {
		panic(err)
	}
	fmt.Println("even squares:", squares)
}
