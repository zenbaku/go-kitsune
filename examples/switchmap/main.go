// Example: switchmap — search-as-you-type with cancellation of stale queries.
//
// Demonstrates: SwitchMap cancelling active inner pipelines when new upstream
// items arrive, so only the latest request's results reach the output.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// simulateSearch pretends to call a backend search API.
// It sleeps briefly to simulate latency, then yields matching results.
func simulateSearch(ctx context.Context, query string, yield func(string) error) error {
	// Simulate variable latency: shorter queries resolve faster.
	latency := time.Duration(50-len(query)*5) * time.Millisecond
	if latency < 5*time.Millisecond {
		latency = 5 * time.Millisecond
	}
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		// Cancelled — a newer query superseded us.
		return ctx.Err()
	}

	// Produce results only if we weren't cancelled.
	corpus := []string{
		"apple", "application", "apply", "apt",
		"banana", "band", "bandana",
		"cherry", "chart", "charm",
	}
	for _, word := range corpus {
		if strings.HasPrefix(word, query) {
			if err := yield(fmt.Sprintf("[%s] → %s", query, word)); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	// Simulate keystrokes: "a", "ap", "app", "appl", "apple"
	// Each new keystroke should cancel the previous search.
	keystrokes := kitsune.FromSlice([]string{"a", "ap", "app", "appl", "apple"})

	results, err := kitsune.SwitchMap(keystrokes, simulateSearch).Collect(context.Background())
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	fmt.Println("=== SwitchMap: search-as-you-type ===")
	fmt.Println("(Only results from queries that weren't superseded reach here)")
	fmt.Println()
	for _, r := range results {
		fmt.Println(" ", r)
	}
	if len(results) == 0 {
		fmt.Println("  (all intermediate queries were cancelled — only final query survived)")
	}

	// Demonstrate with a single slow query to show results do arrive.
	fmt.Println()
	fmt.Println("=== SwitchMap: single query (no cancellation) ===")
	single, err := kitsune.SwitchMap(
		kitsune.FromSlice([]string{"ban"}),
		simulateSearch,
	).Collect(context.Background())
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	for _, r := range single {
		fmt.Println(" ", r)
	}
}
