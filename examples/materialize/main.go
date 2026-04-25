// Example: materialize: error-tolerant pipelines with Materialize / Dematerialize.
//
// A log-ingestion pipeline reads entries from a stream that may fail mid-run
// (e.g. a remote connection drops). Without Materialize the terminal error
// propagates to Run and the caller must inspect it there, after all processing
// is done. With Materialize the error becomes a Notification value that flows
// through the pipeline graph itself, letting downstream stages handle it inline
// and allowing Run to return nil.
//
// Demonstrates:
//   - Materialize: convert a pipeline's terminal error into a Notification value
//   - Dematerialize: re-inject an error notification as a real pipeline error
//   - Handling all three notification kinds (value / error / complete) in ForEach
//   - Roundtrip identity: Dematerialize(Materialize(p)) ≡ p
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// LogEntry is the parsed form of a raw log line.
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
}

// streamResult collects what a pipeline run observed.
type streamResult struct {
	entries []LogEntry
	lastErr error
	done    bool
}

// simulatedStream emits a few log entries and then returns a connection error,
// simulating a remote source that drops mid-stream.
func simulatedStream() *kitsune.Pipeline[LogEntry] {
	entries := []LogEntry{
		{Timestamp: mustParse("2024-03-01T10:00:00"), Level: "INFO", Message: "service started"},
		{Timestamp: mustParse("2024-03-01T10:00:01"), Level: "DEBUG", Message: "listening on :8080"},
		{Timestamp: mustParse("2024-03-01T10:00:02"), Level: "WARN", Message: "high memory usage"},
	}
	errDropped := errors.New("connection dropped")

	return kitsune.Generate(func(_ context.Context, yield func(LogEntry) bool) error {
		for _, e := range entries {
			yield(e)
		}
		return errDropped // simulate a mid-stream failure
	})
}

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Without Materialize: the terminal error propagates to Run.
	// The caller can still read the collected entries, but the error is only
	// visible after Run returns; it is out-of-band from the pipeline graph.
	// -------------------------------------------------------------------------
	fmt.Println("=== Without Materialize ===")

	var plain []LogEntry
	_, err := simulatedStream().
		ForEach(func(_ context.Context, e LogEntry) error {
			plain = append(plain, e)
			return nil
		}).Run(ctx)

	fmt.Printf("  entries collected : %d\n", len(plain))
	fmt.Printf("  Run error         : %v\n\n", err)

	// -------------------------------------------------------------------------
	// With Materialize: the terminal error becomes a Notification[LogEntry]
	// value and flows through the pipeline. ForEach sees every event: values,
	// the error, and the completion signal; and Run returns nil.
	// -------------------------------------------------------------------------
	fmt.Println("=== With Materialize ===")

	var result streamResult
	_, err = kitsune.Materialize(simulatedStream()).
		ForEach(func(_ context.Context, n kitsune.Notification[LogEntry]) error {
			switch {
			case n.IsValue():
				result.entries = append(result.entries, n.Value)
				fmt.Printf("  [entry] %s %s\n", n.Value.Level, n.Value.Message)
			case n.IsError():
				result.lastErr = n.Err
				fmt.Printf("  [error] stream failed: %v\n", n.Err)
			case n.IsComplete():
				result.done = true
				fmt.Println("  [done]  stream complete")
			}
			return nil
		}).Run(ctx)

	fmt.Printf("\n  entries collected : %d\n", len(result.entries))
	fmt.Printf("  captured error    : %v\n", result.lastErr)
	fmt.Printf("  completed cleanly : %v\n", result.done)
	fmt.Printf("  Run error         : %v\n\n", err)

	// -------------------------------------------------------------------------
	// Error roundtrip: Materialize followed by Dematerialize re-injects the
	// error so downstream behaves exactly as if Materialize were not there.
	// -------------------------------------------------------------------------
	fmt.Println("=== Error roundtrip (Dematerialize re-injects the error) ===")

	var roundEntries []LogEntry
	_, roundErr := kitsune.Dematerialize(kitsune.Materialize(simulatedStream())).
		ForEach(func(_ context.Context, e LogEntry) error {
			roundEntries = append(roundEntries, e)
			return nil
		}).Run(ctx)

	fmt.Printf("  entries collected : %d\n", len(roundEntries))
	fmt.Printf("  Run error         : %v\n\n", roundErr)

	// -------------------------------------------------------------------------
	// Roundtrip identity: for error-free streams, the output is identical.
	// -------------------------------------------------------------------------
	fmt.Println("=== Roundtrip identity (error-free stream) ===")

	nums := []int{10, 20, 30, 40, 50}
	got, err := kitsune.Dematerialize(kitsune.Materialize(kitsune.FromSlice(nums))).
		Collect(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Printf("  input  : %v\n", nums)
	fmt.Printf("  output : %v\n", got)
	fmt.Printf("  equal  : %v\n", sliceEq(nums, got))
}

func mustParse(s string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05", s)
	if err != nil {
		panic(err)
	}
	return t
}

func sliceEq[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
