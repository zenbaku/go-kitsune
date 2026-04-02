// Example: concurrent — parallel processing with and without ordering.
//
// Demonstrates: Concurrency option, Ordered option, Buffer option, WithName
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	// --- Unordered: fastest, output arrives in completion order ---
	fmt.Println("=== Unordered (completion order) ===")
	start := time.Now()
	input := kitsune.FromSlice(items)
	processed := kitsune.Map(input, func(_ context.Context, n int) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return fmt.Sprintf("item-%d", n), nil
	},
		kitsune.Concurrency(5),
		kitsune.Buffer(32),
		kitsune.WithName("slow-transform"),
	)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	results, err := processed.Collect(context.Background(), kitsune.WithHook(kitsune.LogHook(logger)))
	if err != nil {
		panic(err)
	}
	fmt.Printf("Processed %d items in %v — order not guaranteed\n\n",
		len(results), time.Since(start).Round(time.Millisecond))

	// --- Ordered: same concurrency, output guaranteed in input order ---
	fmt.Println("=== Ordered (input order preserved) ===")
	start = time.Now()
	input2 := kitsune.FromSlice(items)
	processed2 := kitsune.Map(input2, func(_ context.Context, n int) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return fmt.Sprintf("item-%d", n), nil
	},
		kitsune.Concurrency(5),
		kitsune.Ordered(), // preserve input order while still parallelising
		kitsune.WithName("ordered-transform"),
	)
	ordered, err := processed2.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Processed %d items in %v — order guaranteed: %s … %s\n",
		len(ordered), time.Since(start).Round(time.Millisecond), ordered[0], ordered[len(ordered)-1])
}
