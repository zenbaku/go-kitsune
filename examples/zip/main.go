// Example: zip — pairing two pipeline branches by position.
//
// Demonstrates: Zip, Broadcast, Pair, tracking originals alongside transforms.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

type Record struct {
	ID   int
	Name string
}

func main() {
	// --- Basic zip: original alongside doubled value ---
	fmt.Println("=== Original + Doubled ===")
	branches := kitsune.Broadcast[int](
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}), 2,
	)
	doubled := kitsune.Map(branches[1], func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	pairs, err := kitsune.Zip(branches[0], doubled).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	for _, p := range pairs {
		fmt.Printf("  %d → %d\n", p.First, p.Second)
	}

	// --- Different types: record + normalised name ---
	fmt.Println("\n=== Record + Normalised Name ===")
	records := []Record{{1, "Alice Smith"}, {2, "bob jones"}, {3, "CAROL WHITE"}}
	rb := kitsune.Broadcast[Record](kitsune.FromSlice(records), 2)
	normalised := kitsune.Map(rb[1], func(_ context.Context, r Record) (string, error) {
		return strings.Title(strings.ToLower(r.Name)), nil //nolint:staticcheck
	})
	enriched, _ := kitsune.Zip(rb[0], normalised).Collect(context.Background())
	for _, p := range enriched {
		fmt.Printf("  id=%d  raw=%q  normalised=%q\n", p.First.ID, p.First.Name, p.Second)
	}

	// --- Zip stops at the shorter stream ---
	fmt.Println("\n=== Zip stops at shorter stream ===")
	src := kitsune.FromSlice([]int{10, 20, 30, 40, 50})
	b2 := kitsune.Broadcast[int](src, 2)
	short := b2[0].Take(3) // only 3 items
	full := b2[1]          // all 5 items

	limited, _ := kitsune.Zip(short, full).Collect(context.Background())
	fmt.Printf("  Emitted %d pairs (stopped when short stream ended)\n", len(limited))
	for _, p := range limited {
		fmt.Printf("  (%d, %d)\n", p.First, p.Second)
	}
}
