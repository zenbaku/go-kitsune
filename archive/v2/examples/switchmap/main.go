// Example: switchmap — cancel in-progress work when a newer item arrives.
//
// Demonstrates: SwitchMap cancellation semantics vs FlatMap/ConcatMap
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune/v2"
)

func main() {
	ctx := context.Background()

	// Each "search query" triggers a slow lookup. With SwitchMap, arriving a
	// new query cancels the still-running previous one — only the last query's
	// result is emitted. This models type-ahead search or live-reload.

	queries := kitsune.FromSlice([]string{"g", "go", "gol", "golang"})

	search := func(ctx context.Context, query string, emit func(string) error) error {
		// Simulate a slow network call; check ctx so we respect cancellation.
		select {
		case <-time.After(30 * time.Millisecond):
			return emit(fmt.Sprintf("results for %q", query))
		case <-ctx.Done():
			return nil // cancelled by a newer query
		}
	}

	fmt.Println("=== SwitchMap (only last query completes) ===")
	results, err := kitsune.Collect(ctx, kitsune.SwitchMap(queries, search))
	if err != nil {
		panic(err)
	}
	fmt.Println("received:", results)
	// Only "golang" result arrives because prior queries are cancelled.

	// --- Compare: FlatMap emits all results (no cancellation) ---

	fmt.Println("\n=== FlatMap (all queries complete) ===")
	allResults, err := kitsune.Collect(ctx,
		kitsune.FlatMap(kitsune.FromSlice([]string{"g", "go", "gol", "golang"}),
			func(_ context.Context, query string, emit func(string) error) error {
				time.Sleep(5 * time.Millisecond)
				return emit(fmt.Sprintf("results for %q", query))
			}))
	if err != nil {
		panic(err)
	}
	fmt.Println("received:", allResults)
}
