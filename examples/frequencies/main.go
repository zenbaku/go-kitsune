// Example: frequencies: count item occurrences with Frequencies and FrequenciesBy.
//
// Demonstrates: Frequencies, FrequenciesBy, Single.
package main

import (
	"context"
	"fmt"
	"log"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Frequencies: count each distinct value.
	words := kitsune.FromSlice([]string{"go", "kitsune", "go", "pipeline", "kitsune", "go"})
	counts, err := kitsune.Single(ctx, kitsune.Frequencies(words))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("word counts: %v\n", counts)

	// FrequenciesBy: count by a derived key (word length here).
	lengths, err := kitsune.Single(ctx,
		kitsune.FrequenciesBy(
			kitsune.FromSlice([]string{"go", "kitsune", "go", "pipeline", "kitsune", "go"}),
			func(s string) int { return len(s) },
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("counts by length: %v\n", lengths)
}
