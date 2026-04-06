// Example: scan — stateful running aggregation.
//
// Demonstrates: Scan (running sum, running max, type-changing accumulator),
// TakeWhile, DropWhile.
package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// --- Running sum ---
	fmt.Println("=== Running sum ===")
	sums, err := kitsune.Scan(
		kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		0,
		func(acc, v int) int { return acc + v },
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(sums) // [1 3 6 10 15]

	// --- Running max (type-preserving) ---
	fmt.Println("\n=== Running max ===")
	readings := []int{3, 7, 2, 9, 4, 11, 6}
	maxes, _ := kitsune.Scan(
		kitsune.FromSlice(readings),
		math.MinInt,
		func(max, v int) int {
			if v > max {
				return v
			}
			return max
		},
	).Collect(context.Background())
	fmt.Println(maxes) // [3 7 7 9 9 11 11]

	// --- Type-changing accumulator: collect words into a sentence ---
	fmt.Println("\n=== Running sentence ===")
	words := []string{"the", "quick", "brown", "fox"}
	sentences, _ := kitsune.Scan(
		kitsune.FromSlice(words),
		"",
		func(s, w string) string {
			if s == "" {
				return w
			}
			return s + " " + w
		},
	).Collect(context.Background())
	for _, s := range sentences {
		fmt.Println(s)
	}

	// --- TakeWhile: read until a sentinel value ---
	fmt.Println("\n=== TakeWhile: stop at blank line ===")
	lines := []string{"# header", "key=value", "another=pair", "", "ignored=data"}
	headers, _ := kitsune.TakeWhile(
		kitsune.FromSlice(lines),
		func(s string) bool { return s != "" },
	).Collect(context.Background())
	fmt.Printf("Got %d non-blank lines before the blank\n", len(headers))

	// --- DropWhile: skip comment lines, then emit everything ---
	fmt.Println("\n=== DropWhile: skip comment lines ===")
	config := []string{"# comment", "# another comment", "host=localhost", "port=8080", "# ignored"}
	data, _ := kitsune.DropWhile(
		kitsune.FromSlice(config),
		func(s string) bool { return strings.HasPrefix(s, "#") },
	).Collect(context.Background())
	fmt.Println("Data lines:", data)
}
