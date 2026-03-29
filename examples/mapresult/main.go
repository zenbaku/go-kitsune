// Example: mapresult — route successful and failed items to separate pipelines.
//
// Demonstrates: MapResult, ErrItem, MergeRunners, Map, ForEach.
package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	kitsune "github.com/jonathan/go-kitsune"
)

// parseRecord tries to convert a raw string record into a typed value.
type Record struct {
	Raw   string
	Value int
}

func parseRecord(_ context.Context, raw string) (Record, error) {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return Record{}, fmt.Errorf("invalid record %q: %w", raw, err)
	}
	if v < 0 {
		return Record{}, errors.New("negative values not allowed")
	}
	return Record{Raw: raw, Value: v}, nil
}

func main() {
	// --- MapResult: route successes and errors to separate branches ---
	//
	// Unlike OnError(Skip), MapResult gives you the failed items — with their
	// original input and the error — so you can dead-letter, log, alert, or
	// reprocess them. Both output pipelines must be consumed.
	fmt.Println("=== MapResult: parse records, route bad ones to dead-letter ===")

	rawRecords := []string{"42", "bad", "17", "-5", "99", "???", "0"}

	ok, failed := kitsune.MapResult(kitsune.FromSlice(rawRecords), parseRecord)

	// Good records: double the value.
	var good []Record
	goodRunner := kitsune.Map(ok, func(_ context.Context, r Record) (Record, error) {
		return Record{Raw: r.Raw, Value: r.Value * 2}, nil
	}, kitsune.WithName("double")).ForEach(func(_ context.Context, r Record) error {
		good = append(good, r)
		return nil
	}, kitsune.WithName("collect-good"))

	// Failed records: log to dead-letter queue.
	var deadLetter []kitsune.ErrItem[string]
	failedRunner := failed.ForEach(func(_ context.Context, e kitsune.ErrItem[string]) error {
		deadLetter = append(deadLetter, e)
		return nil
	}, kitsune.WithName("dead-letter"))

	// Both branches must run concurrently — MergeRunners handles this.
	runner, err := kitsune.MergeRunners(goodRunner, failedRunner)
	if err != nil {
		panic(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		panic(err)
	}

	fmt.Printf("Input: %d records\n\n", len(rawRecords))

	fmt.Printf("Processed (%d):\n", len(good))
	for _, r := range good {
		fmt.Printf("  %q → %d (doubled)\n", r.Raw, r.Value)
	}

	fmt.Printf("\nDead-letter (%d):\n", len(deadLetter))
	for _, e := range deadLetter {
		fmt.Printf("  %q → %v\n", e.Item, e.Err)
	}

	// --- MapResult: chain further processing on the error branch ---
	//
	// The failed pipeline is just a *Pipeline[ErrItem[I]] — you can apply
	// any operator to it. Here we extract just the error messages.
	fmt.Println("\n=== MapResult: extract error messages from failed branch ===")

	inputs := []string{"10", "twenty", "30", "forty", "50"}
	ok2, failed2 := kitsune.MapResult(
		kitsune.FromSlice(inputs),
		func(_ context.Context, s string) (int, error) {
			return strconv.Atoi(s)
		},
	)

	nums, _ := ok2.Collect(context.Background())

	errMsgs, _ := kitsune.Map(
		failed2,
		func(_ context.Context, e kitsune.ErrItem[string]) (string, error) {
			return fmt.Sprintf("%q: %v", e.Item, e.Err), nil
		},
	).Collect(context.Background())

	fmt.Printf("Parsed numbers: %v\n", nums)
	fmt.Printf("Parse errors:   %v\n", errMsgs)
}
