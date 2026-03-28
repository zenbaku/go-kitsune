// Example: transform — element-level transforms (Reject, WithIndex, Intersperse,
// TakeEvery, DropEvery, MapEvery, ConsecutiveDedup, ConsecutiveDedupBy).
//
// Demonstrates: filtering inverses, positional selection, separator insertion,
// and consecutive deduplication.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Reject: keep items that do NOT satisfy the predicate (inverse of Filter).
	// -------------------------------------------------------------------------
	fmt.Println("=== Reject: remove even numbers ===")
	odds, _ := kitsune.Reject(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
		func(n int) bool { return n%2 == 0 },
	).Collect(ctx)
	fmt.Println("Odds:", odds)

	// -------------------------------------------------------------------------
	// WithIndex: attach 0-based position index to each item.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== WithIndex: enumerate log lines ===")
	lines := []string{"Starting up", "Connected to DB", "Processing requests", "Shutting down"}
	indexed, _ := kitsune.WithIndex(kitsune.FromSlice(lines)).Collect(ctx)
	for _, p := range indexed {
		fmt.Printf("  [%d] %s\n", p.First, p.Second)
	}

	// -------------------------------------------------------------------------
	// Intersperse: insert a separator between items.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== Intersperse: join words with separator ===")
	words, _ := kitsune.Intersperse(
		kitsune.FromSlice([]string{"SELECT", "*", "FROM", "users"}),
		" ",
	).Collect(ctx)
	fmt.Println("SQL:", strings.Join(words, ""))

	// -------------------------------------------------------------------------
	// TakeEvery / DropEvery: select or remove every nth item (0-indexed).
	// -------------------------------------------------------------------------
	fmt.Println("\n=== TakeEvery(3): sample 1 in 3 ===")
	nums := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	sampled, _ := kitsune.TakeEvery(kitsune.FromSlice(nums), 3).Collect(ctx)
	fmt.Println("Sampled:", sampled)

	fmt.Println("\n=== DropEvery(4): drop header rows ===")
	rows := []string{"H1", "r1", "r2", "r3", "H2", "r4", "r5", "r6", "H3", "r7"}
	data, _ := kitsune.DropEvery(kitsune.FromSlice(rows), 4).Collect(ctx)
	fmt.Println("Data rows:", data)

	// -------------------------------------------------------------------------
	// MapEvery: apply a transform to every nth item, pass others unchanged.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== MapEvery(3): uppercase every 3rd word ===")
	sentence := []string{"the", "quick", "brown", "fox", "jumps", "over", "the", "lazy", "dog"}
	result, _ := kitsune.MapEvery(
		kitsune.FromSlice(sentence), 3,
		func(_ context.Context, s string) (string, error) { return strings.ToUpper(s), nil },
	).Collect(ctx)
	fmt.Println("Result:", result)

	// -------------------------------------------------------------------------
	// ConsecutiveDedup: remove runs of identical values.
	// Unlike Distinct, non-adjacent duplicates are kept.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== ConsecutiveDedup: compress run-length encoded data ===")
	sensor := []int{0, 0, 0, 1, 1, 2, 2, 2, 2, 1, 1, 0}
	deduped, _ := kitsune.ConsecutiveDedup(kitsune.FromSlice(sensor)).Collect(ctx)
	fmt.Println("Transitions:", deduped)

	// -------------------------------------------------------------------------
	// ConsecutiveDedupBy: dedup by a derived key — useful for non-comparable types.
	// -------------------------------------------------------------------------
	fmt.Println("\n=== ConsecutiveDedupBy: collapse repeated event types ===")
	type Event struct{ Type, ID string }
	events := []Event{
		{"click", "btn1"}, {"click", "btn2"}, {"scroll", "p1"},
		{"scroll", "p2"}, {"scroll", "p3"}, {"click", "btn3"},
	}
	collapsed, _ := kitsune.ConsecutiveDedupBy(
		kitsune.FromSlice(events),
		func(e Event) string { return e.Type },
	).Collect(ctx)
	for _, e := range collapsed {
		fmt.Printf("  %s (%s)\n", e.Type, e.ID)
	}
}
