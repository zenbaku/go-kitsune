package kitsune_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func TestOrderedMap(t *testing.T) {
	// 100 items with variable sleep; Concurrency(8) + Ordered() must emit in input order.
	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		if v%2 == 1 {
			time.Sleep(time.Millisecond)
		}
		return v, nil
	}, kitsune.Concurrency(8), kitsune.Ordered()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
	for i, v := range results {
		if v != i {
			t.Fatalf("position %d: want %d, got %d", i, i, v)
		}
	}
}

func TestOrderedMapWithSkip(t *testing.T) {
	// Items that error+skip are dropped; remaining items stay in input order.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		if v%3 == 0 {
			return 0, fmt.Errorf("skip-me")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered(), kitsune.OnError(kitsune.Skip())).
		Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(results); i++ {
		if results[i] <= results[i-1] {
			t.Fatalf("order violation at index %d: %d not > %d", i, results[i], results[i-1])
		}
	}
	for _, v := range results {
		if v%3 == 0 {
			t.Fatalf("skipped item %d appeared in results", v)
		}
	}
}

func TestOrderedMapWithHalt(t *testing.T) {
	// An error with Halt must stop the pipeline without deadlock.
	items := make([]int, 50)
	for i := range items {
		items[i] = i
	}
	_, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		if v == 10 {
			return 0, fmt.Errorf("halt-error")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered()).Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOrderedFlatMap(t *testing.T) {
	// FlatMap with Concurrency + Ordered: output order must match input order.
	items := make([]int, 30)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.FlatMap(kitsune.FromSlice(items), func(_ context.Context, v int, yield func(int) error) error {
		if v%2 == 1 {
			time.Sleep(time.Millisecond)
		}
		if err := yield(v * 10); err != nil {
			return err
		}
		return yield(v*10 + 1)
	}, kitsune.Concurrency(4), kitsune.Ordered()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items)*2 {
		t.Fatalf("want %d results, got %d", len(items)*2, len(results))
	}
	for i, v := range results {
		expected := (i/2)*10 + (i % 2)
		if v != expected {
			t.Fatalf("position %d: want %d, got %d", i, expected, v)
		}
	}
}

func TestOrderedContextCancellation(t *testing.T) {
	// No deadlock when context is cancelled during ordered concurrent processing.
	ctx, cancel := context.WithCancel(context.Background())
	items := make([]int, 200)
	for i := range items {
		items[i] = i
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) { //nolint:errcheck
			time.Sleep(5 * time.Millisecond)
			return v, nil
		}, kitsune.Concurrency(8), kitsune.Ordered()).Collect(ctx) //nolint:errcheck
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not exit after context cancellation")
	}
}

func TestOrderedRetry(t *testing.T) {
	// A retried item ends up in the correct position after retry succeeds.
	var attempt [10]int
	items := make([]int, 10)
	for i := range items {
		items[i] = i
	}
	results, err := kitsune.Map(kitsune.FromSlice(items), func(_ context.Context, v int) (int, error) {
		attempt[v]++
		if v == 5 && attempt[v] < 3 {
			return 0, fmt.Errorf("transient")
		}
		return v, nil
	}, kitsune.Concurrency(4), kitsune.Ordered(),
		kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(0))),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 10 {
		t.Fatalf("want 10 results, got %d", len(results))
	}
	for i, v := range results {
		if v != i {
			t.Fatalf("position %d: want %d, got %d", i, i, v)
		}
	}
}

func TestOrderedCancellationNoLeak(t *testing.T) {
	// Cancelling an ordered concurrent stage must not deadlock.
	ctx, cancel := context.WithCancel(context.Background())

	const n = 1000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Map(
			kitsune.FromSlice(items),
			func(_ context.Context, v int) (int, error) { return v, nil },
			kitsune.Concurrency(8),
			kitsune.Ordered(),
		).Collect(ctx)
		done <- err
	}()

	time.Sleep(time.Millisecond)
	cancel()

	select {
	case <-done:
		// Pipeline terminated — no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("ordered pipeline deadlocked under cancellation")
	}
}
