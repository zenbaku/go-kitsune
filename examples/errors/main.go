// Example: errors — handling failures with skip, retry, and fallback.
//
// Demonstrates: OnError, Skip, Retry, RetryThen, FixedBackoff, ExponentialBackoff
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	// --- Skip: drop bad items, continue processing ---
	fmt.Println("=== Skip errors ===")
	input := kitsune.FromSlice([]string{"1", "two", "3", "four", "5"})
	parsed := kitsune.Map(input, func(_ context.Context, s string) (int, error) {
		if s[0] < '0' || s[0] > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		return int(s[0] - '0'), nil
	}, kitsune.OnError(kitsune.Skip()))

	results, err := parsed.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Parsed (skipped bad):", results)

	// --- Retry: transient failures succeed on retry ---
	fmt.Println("\n=== Retry with fixed backoff ===")
	var attempts atomic.Int32
	flaky := kitsune.FromSlice([]string{"hello"})
	retried := kitsune.Map(flaky, func(_ context.Context, s string) (string, error) {
		n := attempts.Add(1)
		fmt.Printf("  attempt %d\n", n)
		if n < 3 {
			return "", errors.New("transient failure")
		}
		return s + "!", nil
	}, kitsune.OnError(kitsune.Retry(5, kitsune.FixedBackoff(10*time.Millisecond))))

	results2, err := retried.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Result:", results2)

	// --- RetryThen: retry, then skip if still failing ---
	fmt.Println("\n=== RetryThen (retry 2x, then skip) ===")
	items := kitsune.FromSlice([]int{1, 2, 3})
	mapped := kitsune.Map(items, func(_ context.Context, n int) (string, error) {
		if n == 2 {
			return "", errors.New("always fails")
		}
		return fmt.Sprintf("ok-%d", n), nil
	}, kitsune.OnError(kitsune.RetryThen(
		2,
		kitsune.ExponentialBackoff(5*time.Millisecond, 50*time.Millisecond),
		kitsune.Skip(),
	)))

	results3, err := mapped.Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Results (item 2 skipped after retries):", results3)
}
