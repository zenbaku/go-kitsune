// Example: filter — selecting, observing, and limiting items.
//
// Demonstrates: Filter, Tap, Take, Drain
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
	logs := []LogEntry{
		{"INFO", "server started"},
		{"ERROR", "connection refused"},
		{"DEBUG", "cache miss"},
		{"ERROR", "timeout"},
		{"INFO", "request handled"},
		{"ERROR", "disk full"},
	}

	// Collect: filter, observe, and return matching items.
	input := kitsune.FromSlice(logs)
	results, err := input.
		Filter(func(e LogEntry) bool { return e.Level == "ERROR" }).
		Tap(func(e LogEntry) { fmt.Printf("  [seen] %s\n", e.Message) }).
		Take(2).
		Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("First 2 errors: %v\n\n", results)

	// Drain: run purely for side effects — Tap logs each error, no results needed.
	input2 := kitsune.FromSlice(logs)
	if err := input2.
		Filter(func(e LogEntry) bool { return e.Level == "ERROR" }).
		Tap(func(e LogEntry) { fmt.Printf("  [alert] %s\n", e.Message) }).
		Drain().Run(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("All errors alerted.")
}
