// Example: flatmap — one-to-many expansion.
//
// Demonstrates: FlatMap for splitting, pagination, and nested iteration.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Split strings into words ---
	fmt.Println("=== Split sentences into words ===")
	sentences := kitsune.FromSlice([]string{
		"the quick brown fox",
		"jumps over",
		"the lazy dog",
	})
	words := kitsune.FlatMap(sentences, func(_ context.Context, s string, yield func(string) error) error {
		for _, w := range strings.Fields(s) {
			if err := yield(w); err != nil {
				return err
			}
		}
		return nil
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
	allItems := kitsune.FlatMap(pages, func(_ context.Context, p Page, yield func(string) error) error {
		fmt.Printf("  expanding page %d (%d items)\n", p.Number, len(p.Items))
		for _, item := range p.Items {
			if err := yield(item); err != nil {
				return err
			}
		}
		return nil
	})
	items, _ := allItems.Collect(context.Background())
	fmt.Println("All items:", items)

	// --- Generate permutations ---
	fmt.Println("\n=== Generate pairs ===")
	colors := kitsune.FromSlice([]string{"red", "blue"})
	pairs := kitsune.FlatMap(colors, func(_ context.Context, color string, yield func(string) error) error {
		for _, size := range []string{"S", "M", "L"} {
			if err := yield(color + "-" + size); err != nil {
				return err
			}
		}
		return nil
	})
	combos, _ := pairs.Collect(context.Background())
	fmt.Println(combos)
}
