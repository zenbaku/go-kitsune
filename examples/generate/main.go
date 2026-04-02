// Example: generate — creating pipelines from custom sources.
//
// Demonstrates: Generate for paginated APIs, infinite streams, and custom iteration.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Paginated API simulation ---
	fmt.Println("=== Paginated source ===")
	pages := map[string][]string{
		"":    {"alice", "bob", "carol"},
		"pg2": {"dave", "eve"},
		"pg3": {"frank"},
	}
	nextPage := map[string]string{
		"":    "pg2",
		"pg2": "pg3",
		"pg3": "",
	}

	paginated := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		cursor := ""
		for {
			page := pages[cursor]
			fmt.Printf("  fetching page %q (%d items)\n", cursor, len(page))
			for _, item := range page {
				if !yield(item) {
					return nil
				}
			}
			cursor = nextPage[cursor]
			if cursor == "" {
				return nil
			}
		}
	})

	results, _ := paginated.Collect(context.Background())
	fmt.Println("All users:", results)

	// --- Ticker / infinite stream with Take ---
	fmt.Println("\n=== Ticker (take 5) ===")
	ticker := kitsune.Generate(func(ctx context.Context, yield func(time.Time) bool) error {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case t := <-tick.C:
				if !yield(t) {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	ticks, _ := ticker.Take(5).Collect(context.Background())
	for i, t := range ticks {
		fmt.Printf("  tick %d: %s\n", i, t.Format("15:04:05.000"))
	}

	// --- Counter ---
	fmt.Println("\n=== Counter (take 8) ===")
	counter := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})

	nums, _ := counter.Take(8).Collect(context.Background())
	fmt.Println("Numbers:", nums)
}
