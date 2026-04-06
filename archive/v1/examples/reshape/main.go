// Example: reshape — structural pipeline transforms (ChunkBy, ChunkWhile, Sort, SortBy, Unzip).
//
// Demonstrates: grouping consecutive items by key or predicate, sorting streams,
// and splitting a pair stream into two separate streams.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

type LogEntry struct {
	Level   string
	Message string
}

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// ChunkBy: group consecutive items that share the same key.
	// -------------------------------------------------------------------------
	fmt.Println("=== ChunkBy: group log entries by level ===")
	logs := []LogEntry{
		{"INFO", "Server started"},
		{"INFO", "Listening on :8080"},
		{"WARN", "High memory usage"},
		{"ERROR", "Connection refused"},
		{"ERROR", "Retrying..."},
		{"INFO", "Retry succeeded"},
	}
	chunks, _ := kitsune.ChunkBy(kitsune.FromSlice(logs),
		func(e LogEntry) string { return e.Level },
	).Collect(ctx)
	for _, chunk := range chunks {
		fmt.Printf("  [%s] %d message(s):", chunk[0].Level, len(chunk))
		for _, e := range chunk {
			fmt.Printf(" %q", e.Message)
		}
		fmt.Println()
	}

	// -------------------------------------------------------------------------
	// ChunkWhile: group consecutive items while a pairwise predicate holds.
	// Great for detecting runs of adjacent values.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== ChunkWhile: contiguous date ranges ===")
	days := []int{1, 2, 3, 7, 8, 12, 13, 14, 15}
	ranges, _ := kitsune.ChunkWhile(kitsune.FromSlice(days),
		func(prev, next int) bool { return next-prev == 1 },
	).Collect(ctx)
	for _, r := range ranges {
		if len(r) == 1 {
			fmt.Printf("  day %d\n", r[0])
		} else {
			fmt.Printf("  days %d–%d\n", r[0], r[len(r)-1])
		}
	}

	// -------------------------------------------------------------------------
	// Sort / SortBy: buffer and re-emit in sorted order.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Sort: sort scores descending ===")
	scores := []int{42, 7, 99, 13, 58, 71, 3, 88}
	sorted, _ := kitsune.Sort(kitsune.FromSlice(scores),
		func(a, b int) bool { return a > b }, // descending
	).Collect(ctx)
	fmt.Println("Top scores:", sorted)

	fmt.Println("\n=== SortBy: sort products by price ascending ===")
	type Product struct {
		Name  string
		Price float64
	}
	products := []Product{
		{"Laptop", 999.99},
		{"Mouse", 29.99},
		{"Monitor", 349.99},
		{"Keyboard", 79.99},
	}
	byPrice, _ := kitsune.SortBy(kitsune.FromSlice(products),
		func(p Product) float64 { return p.Price },
		func(a, b float64) bool { return a < b },
	).Collect(ctx)
	for i, p := range byPrice {
		fmt.Printf("  %d. %-10s $%.2f\n", i+1, p.Name, p.Price)
	}

	// -------------------------------------------------------------------------
	// Unzip: split a Pair stream into two separate streams.
	// Inverse of Zip — useful after ZipWith produces Pair values.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Unzip: split key-value pairs ===")
	kvPairs := []kitsune.Pair[string, int]{
		{First: "alpha", Second: 1},
		{First: "beta", Second: 2},
		{First: "gamma", Second: 3},
		{First: "delta", Second: 4},
	}
	keys, values := kitsune.Unzip(kitsune.FromSlice(kvPairs))
	ks, _ := keys.Collect(ctx)
	vs, _ := values.Collect(ctx)
	fmt.Println("Keys:  ", ks)
	fmt.Println("Values:", vs)
}
