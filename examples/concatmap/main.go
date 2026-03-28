// Example: concatmap — sequential expansion that preserves input order.
//
// Demonstrates: ConcatMap vs FlatMap ordering, Collect.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- ConcatMap: guaranteed sequential, order-preserving expansion ---
	//
	// ConcatMap is like FlatMap but forces Concurrency(1), ensuring each
	// input item is fully expanded before the next one is processed.
	// Output order always matches input order — useful when the sub-sequence
	// emitted per item must appear contiguously in the output.
	fmt.Println("=== ConcatMap: expand words into their letters (ordered) ===")

	words := []string{"cat", "dog", "elk"}
	letters, err := kitsune.ConcatMap(
		kitsune.FromSlice(words),
		func(_ context.Context, word string) ([]string, error) {
			chars := make([]string, len(word))
			for i, ch := range word {
				chars[i] = string(ch)
			}
			return chars, nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Input:  %v\n", words)
	fmt.Printf("Output: %v\n", letters)
	// Always: [c a t d o g e l k] — groups never interleave

	// --- ConcatMap: ordered numbered sub-sequences ---
	//
	// Each input item n expands to [n, n*10, n*100]. ConcatMap guarantees
	// these three values appear together before the next item's values.
	fmt.Println("\n=== ConcatMap: sub-sequences stay contiguous ===")

	seqs, err := kitsune.ConcatMap(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) ([]int, error) {
			return []int{n, n * 10, n * 100}, nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Output: %v\n", seqs)
	// Always: [1 10 100 2 20 200 3 30 300]

	// --- ConcatMap: sentence tokeniser ---
	//
	// Split each sentence into tokens while preserving the inter-sentence order.
	fmt.Println("\n=== ConcatMap: tokenise sentences in order ===")

	sentences := []string{
		"hello world",
		"foo bar baz",
		"one two",
	}
	tokens, err := kitsune.ConcatMap(
		kitsune.FromSlice(sentences),
		func(_ context.Context, s string) ([]string, error) {
			return strings.Fields(s), nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Println("Sentences:", sentences)
	fmt.Println("Tokens:   ", tokens)
}
