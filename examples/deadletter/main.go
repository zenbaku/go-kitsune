// Example: deadletter — route exhausted failures to a dead-letter pipeline.
//
// Demonstrates: DeadLetter, DeadLetterSink, ErrItem, Retry, MergeRunners.
package main

import (
	"context"
	"errors"
	"fmt"

	kitsune "github.com/jonathan/go-kitsune"
)

var errTransient = errors.New("transient failure")

func main() {
	// --- DeadLetter: retry embedded, exhausted items route to dlq ---
	//
	// Unlike MapResult (which has no retry), DeadLetter runs the retry loop
	// internally. Items that succeed on any attempt go to ok; items that
	// exhaust all retries go to dlq as ErrItem[Input].
	fmt.Println("=== DeadLetter: process with retry, route failures to dlq ===")

	attempts := map[int]int{}
	inputs := []int{1, 2, 3, 4, 5}

	ok, dlq := kitsune.DeadLetter(
		kitsune.FromSlice(inputs),
		func(_ context.Context, v int) (string, error) {
			attempts[v]++
			// Items 2 and 4 always fail (exhaust retries).
			if v == 2 || v == 4 {
				return "", fmt.Errorf("item %d: %w", v, errTransient)
			}
			return fmt.Sprintf("ok:%d", v), nil
		},
		kitsune.WithName("process"),
		kitsune.OnError(kitsune.Retry(2, kitsune.FixedBackoff(0))),
	)

	var good []string
	var bad []kitsune.ErrItem[int]

	goodRunner := ok.ForEach(func(_ context.Context, s string) error {
		good = append(good, s)
		return nil
	}, kitsune.WithName("collect-good"))

	dlqRunner := dlq.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error {
		bad = append(bad, e)
		return nil
	}, kitsune.WithName("collect-dlq"))

	merged1, err := kitsune.MergeRunners(goodRunner, dlqRunner)
	if err != nil {
		panic(err)
	}
	if err := merged1.Run(context.Background()); err != nil {
		panic(err)
	}

	fmt.Printf("Successful (%d): %v\n", len(good), good)
	fmt.Printf("Dead-lettered (%d):\n", len(bad))
	for _, e := range bad {
		fmt.Printf("  item=%d err=%v\n", e.Item, e.Err)
	}

	// --- DeadLetterSink: terminal sink with retry and dlq ---
	//
	// DeadLetterSink is the sink variant. The dlq pipeline must be consumed
	// before calling runner.Run.
	fmt.Println("\n=== DeadLetterSink: sink with retry, collect failures ===")

	sinkInputs := []int{10, 20, 30}
	sinkAttempts := map[int]int{}

	dlq2, runner := kitsune.DeadLetterSink(
		kitsune.FromSlice(sinkInputs),
		func(_ context.Context, v int) error {
			sinkAttempts[v]++
			if v == 20 {
				return fmt.Errorf("sink rejected %d", v)
			}
			fmt.Printf("  sink processed %d\n", v)
			return nil
		},
		kitsune.WithName("sink"),
		kitsune.OnError(kitsune.Retry(1, kitsune.FixedBackoff(0))),
	)

	var sinkFailed []kitsune.ErrItem[int]
	dlqRunner2 := dlq2.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error {
		sinkFailed = append(sinkFailed, e)
		return nil
	}, kitsune.WithName("collect-sink-dlq"))

	merged2, err := kitsune.MergeRunners(runner, dlqRunner2)
	if err != nil {
		panic(err)
	}
	if err := merged2.Run(context.Background()); err != nil {
		panic(err)
	}

	fmt.Printf("Sink failures (%d):\n", len(sinkFailed))
	for _, e := range sinkFailed {
		fmt.Printf("  item=%d err=%v\n", e.Item, e.Err)
	}
}
