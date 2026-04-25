package kitsune_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// TestRunnable_MergeRunnersAcceptsForEachRunner verifies that *ForEachRunner[T]
// can be passed directly to MergeRunners without calling Build().
func TestRunnable_MergeRunnersAcceptsForEachRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	evens, odds := kitsune.Partition(p, func(v int) bool { return v%2 == 0 })

	var mu sync.Mutex
	var evenItems, oddItems []int

	runner, err := kitsune.MergeRunners(
		evens.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			evenItems = append(evenItems, v)
			mu.Unlock()
			return nil
		}),
		odds.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			oddItems = append(oddItems, v)
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(evenItems)
	sort.Ints(oddItems)
	if !sliceEqual(evenItems, []int{2, 4, 6}) {
		t.Fatalf("evens: got %v, want [2 4 6]", evenItems)
	}
	if !sliceEqual(oddItems, []int{1, 3, 5}) {
		t.Fatalf("odds: got %v, want [1 3 5]", oddItems)
	}
}

// TestRunnable_MergeRunnersAcceptsRunner verifies backwards compatibility:
// *Runner (obtained via Build()) is still accepted by MergeRunners.
func TestRunnable_MergeRunnersAcceptsRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := kitsune.FromSlice([]int{1, 2, 3, 4})
	evens, odds := kitsune.Partition(p, func(v int) bool { return v%2 == 0 })

	var mu sync.Mutex
	var evenItems, oddItems []int

	runner, err := kitsune.MergeRunners(
		evens.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			evenItems = append(evenItems, v)
			mu.Unlock()
			return nil
		}).Build(),
		odds.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			oddItems = append(oddItems, v)
			mu.Unlock()
			return nil
		}).Build(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(evenItems)
	sort.Ints(oddItems)
	if !sliceEqual(evenItems, []int{2, 4}) {
		t.Fatalf("evens: got %v, want [2 4]", evenItems)
	}
	if !sliceEqual(oddItems, []int{1, 3}) {
		t.Fatalf("odds: got %v, want [1 3]", oddItems)
	}
}

// TestRunnable_MergeRunnersMixed verifies that *ForEachRunner[T] and *Runner
// can be mixed in the same MergeRunners call.
func TestRunnable_MergeRunnersMixed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := kitsune.FromSlice([]int{10, 20, 30, 40, 50})
	evens, odds := kitsune.Partition(p, func(v int) bool { return v%20 == 0 })

	var mu sync.Mutex
	var evenItems, oddItems []int

	// One branch uses ForEachRunner directly; the other calls Build().
	runner, err := kitsune.MergeRunners(
		evens.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			evenItems = append(evenItems, v)
			mu.Unlock()
			return nil
		}),
		odds.ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			oddItems = append(oddItems, v)
			mu.Unlock()
			return nil
		}).Build(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(evenItems)
	sort.Ints(oddItems)
	if !sliceEqual(evenItems, []int{20, 40}) {
		t.Fatalf("evens (multiples of 20): got %v, want [20 40]", evenItems)
	}
	if !sliceEqual(oddItems, []int{10, 30, 50}) {
		t.Fatalf("odds: got %v, want [10 30 50]", oddItems)
	}
}

// TestRunnable_ForEachRunnerRunAsync verifies that RunAsync can be called
// directly on a *ForEachRunner[T] without going through Build().
func TestRunnable_ForEachRunnerRunAsync(t *testing.T) {
	var mu sync.Mutex
	var got []int

	handle := kitsune.FromSlice([]int{1, 2, 3, 4, 5}).
		ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			got = append(got, v)
			mu.Unlock()
			return nil
		}).RunAsync(context.Background())

	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not complete in time")
	}
	if _, err := handle.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Ints(got)
	if !sliceEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v, want [1 2 3 4 5]", got)
	}
}
