// Example: flatmap — one-to-many expansion.
//
// Demonstrates: FlatMap for splitting, pagination, and nested iteration.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Split strings into words ---
	fmt.Println("=== Split sentences into words ===")
	sentences := kitsune.FromSlice([]string{
		"the quick brown fox",
		"jumps over",
		"the lazy dog",
	})
	words := kitsune.FlatMap(sentences, func(_ context.Context, s string) ([]string, error) {
		return strings.Fields(s), nil
	})
	result, _ := words.Collect(context.Background())
	fmt.Println(result)

	// --- Simulate paginated API ---
	fmt.Println("\n=== Paginated expansion ===")
	type Page struct {
		Number int
		Items  []string
	}
	pages := kitsune.FromSlice([]Page{
		{1, []string{"a", "b", "c"}},
		{2, []string{"d", "e"}},
		{3, []string{"f"}},
	})
	allItems := kitsune.FlatMap(pages, func(_ context.Context, p Page) ([]string, error) {
		fmt.Printf("  expanding page %d (%d items)\n", p.Number, len(p.Items))
		return p.Items, nil
	})
	items, _ := allItems.Collect(context.Background())
	fmt.Println("All items:", items)

	// --- Generate permutations ---
	fmt.Println("\n=== Generate pairs ===")
	colors := kitsune.FromSlice([]string{"red", "blue"})
	pairs := kitsune.FlatMap(colors, func(_ context.Context, color string) ([]string, error) {
		sizes := []string{"S", "M", "L"}
		out := make([]string, len(sizes))
		for i, size := range sizes {
			out[i] = color + "-" + size
		}
		return out, nil
	})
	combos, _ := pairs.Collect(context.Background())
	fmt.Println(combos)
}
