// Example: concurrent: parallel processing with and without ordering guarantees.
//
// Demonstrates: Concurrency, Ordered, Buffer, WithName
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	simulate := func(_ context.Context, n int) (string, error) {
		time.Sleep(10 * time.Millisecond)
		return fmt.Sprintf("item-%02d", n), nil
	}

	// --- Unordered: fastest, output arrives in completion order ---

	fmt.Println("=== Unordered (completion order) ===")
	start := time.Now()
	unordered, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(items), simulate,
			kitsune.Concurrency(5),
			kitsune.Buffer(32),
			kitsune.WithName("parallel"),
		))
	if err != nil {
		panic(err)
	}
	fmt.Printf("  processed %d items in %v\n\n", len(unordered), time.Since(start).Round(time.Millisecond))

	// --- Ordered: same concurrency, output in original input order ---

	fmt.Println("=== Ordered (input order preserved) ===")
	start = time.Now()
	ordered, err := kitsune.Collect(ctx,
		kitsune.Map(kitsune.FromSlice(items), simulate,
			kitsune.Concurrency(5),
			kitsune.Ordered(),
			kitsune.WithName("parallel-ordered"),
		))
	if err != nil {
		panic(err)
	}
	fmt.Printf("  processed %d items in %v\n", len(ordered), time.Since(start).Round(time.Millisecond))
	fmt.Printf("  first=%s last=%s\n", ordered[0], ordered[len(ordered)-1])
}
