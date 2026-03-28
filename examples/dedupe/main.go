// Example: dedupe — deduplication and cached map lookups.
//
// Demonstrates: Pipeline.Dedupe with default MemoryDedupSet,
// Map with the Cache stage option for transparent result caching.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

type Event struct {
	ID      string
	Payload string
}

func main() {
	// --- Dedupe: drop duplicate events by ID ---
	fmt.Println("=== Deduplication ===")
	events := kitsune.FromSlice([]Event{
		{"e1", "click"},
		{"e2", "scroll"},
		{"e1", "click (duplicate)"},
		{"e3", "hover"},
		{"e2", "scroll (duplicate)"},
		{"e4", "submit"},
	})

	// No DedupSet argument → uses MemoryDedupSet by default.
	unique := events.Dedupe(func(e Event) string { return e.ID })

	results, err := unique.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for _, e := range results {
		fmt.Printf("  %s: %s\n", e.ID, e.Payload)
	}
	fmt.Printf("  → %d unique out of 6 total\n", len(results))

	// --- Map + Cache: expensive lookups cached by key ---
	fmt.Println("\n=== Cached lookups ===")
	callCount := 0
	ids := kitsune.FromSlice([]string{"user-1", "user-2", "user-1", "user-3", "user-1", "user-2"})

	enriched := kitsune.Map(ids,
		func(_ context.Context, id string) (string, error) {
			callCount++
			fmt.Printf("  [cache miss] fetching %s\n", id)
			return fmt.Sprintf("profile(%s)", id), nil
		},
		kitsune.CacheBy(
			func(id string) string { return id },
			kitsune.CacheBackend(kitsune.MemoryCache(100)),
			kitsune.CacheTTL(5*time.Minute),
		),
	)

	profiles, err := enriched.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("  → %d results, %d actual lookups (rest served from cache)\n", len(profiles), callCount)
}
