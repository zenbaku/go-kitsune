// Example: window — time-based batching.
//
// Demonstrates: Window (collects items into slices based on time, not count).
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// Simulate a bursty source: 3 items fast, pause, 2 more fast.
	source := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		for _, s := range []string{"a", "b", "c"} {
			if !yield(s) {
				return nil
			}
		}
		// Pause — window will flush the first batch.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		for _, s := range []string{"d", "e"} {
			if !yield(s) {
				return nil
			}
		}
		return nil
	})

	// Collect items in 100ms windows.
	windows := kitsune.Window(source, 100*time.Millisecond)

	results, err := windows.Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for i, w := range results {
		fmt.Printf("Window %d: %v\n", i, w)
	}
	// Expected: Window 0: [a b c], Window 1: [d e]
	// (exact grouping depends on timing)
}
