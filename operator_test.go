package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Map
// ---------------------------------------------------------------------------

func TestMapSerial(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}))
	want := []int{2, 4, 6, 8, 10}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapConcurrent(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}, kitsune.Concurrency(4)))
	sort.Ints(got)
	want := []int{2, 4, 6, 8, 10, 12, 14, 16}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapConcurrentOrdered(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (string, error) {
		return fmt.Sprintf("item-%d", v), nil
	}, kitsune.Concurrency(3), kitsune.Ordered()))
	want := []string{"item-1", "item-2", "item-3", "item-4", "item-5"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapError(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	boom := errors.New("boom")
	err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, boom
		}
		return v, nil
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMapSkip(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v%2 == 0 {
			return 0, errors.New("even")
		}
		return v, nil
	}, kitsune.OnError(kitsune.Skip())))
	want := []int{1, 3, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapReturn(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, errors.New("bad")
		}
		return v * 10, nil
	}, kitsune.OnError(kitsune.Return(99))))
	want := []int{10, 99, 30}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMapTimeout(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := kitsune.Map(p, func(ctx context.Context, v int) (int, error) {
		if v == 2 {
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return v, nil
	}, kitsune.Timeout(50*time.Millisecond)).
		ForEach(func(_ context.Context, _ int) error { return nil }).
		Run(ctx)

	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ---------------------------------------------------------------------------
// FlatMap
// ---------------------------------------------------------------------------

func TestFlatMapSerial(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.FlatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*10 + i); err != nil {
				return err
			}
		}
		return nil
	}))
	want := []int{10, 20, 21, 30, 31, 32}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFlatMapConcurrentOrdered(t *testing.T) {
	p := kitsune.FromSlice([]int{3, 1, 2})
	got := collectAll(t, kitsune.FlatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*100 + i); err != nil {
				return err
			}
		}
		return nil
	}, kitsune.Concurrency(3), kitsune.Ordered()))
	// Must be ordered: 3→{300,301,302}, 1→{100}, 2→{200,201}
	want := []int{300, 301, 302, 100, 200, 201}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Filter
// ---------------------------------------------------------------------------

func TestFilter(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	got := collectAll(t, kitsune.Filter(p, func(_ context.Context, v int) (bool, error) {
		return v%2 == 0, nil
	}))
	want := []int{2, 4, 6}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Tap
// ---------------------------------------------------------------------------

func TestTap(t *testing.T) {
	var tapped []int
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.Tap(p, func(_ context.Context, v int) error {
		tapped = append(tapped, v*10)
		return nil
	}))
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("tap changed items: got %v", got)
	}
	if !sliceEqual(tapped, []int{10, 20, 30}) {
		t.Fatalf("tap side effect: got %v", tapped)
	}
}

// ---------------------------------------------------------------------------
// Take / Drop
// ---------------------------------------------------------------------------

func TestTake(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Take(p, 3))
	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTakeMoreThanAvailable(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2})
	got := collectAll(t, kitsune.Take(p, 10))
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDrop(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Drop(p, 2))
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSkip(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, p.Skip(2))
	want := []int{3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// TakeWhile / DropWhile
// ---------------------------------------------------------------------------

func TestTakeWhile(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 1, 2})
	got := collectAll(t, kitsune.TakeWhile(p, func(v int) bool { return v < 4 }))
	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDropWhile(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 1, 2})
	got := collectAll(t, kitsune.DropWhile(p, func(v int) bool { return v < 4 }))
	want := []int{4, 1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Batch / Unbatch / Window / SlidingWindow
// ---------------------------------------------------------------------------

func TestBatch(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Batch(p, 2))
	if len(got) != 3 {
		t.Fatalf("expected 3 batches, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2}) {
		t.Errorf("batch 0: got %v", got[0])
	}
	if !sliceEqual(got[1], []int{3, 4}) {
		t.Errorf("batch 1: got %v", got[1])
	}
	if !sliceEqual(got[2], []int{5}) {
		t.Errorf("batch 2 (partial): got %v", got[2])
	}
}

func TestUnbatch(t *testing.T) {
	p := kitsune.FromSlice([][]int{{1, 2}, {3, 4, 5}})
	got := collectAll(t, kitsune.Unbatch(p))
	want := []int{1, 2, 3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWindow(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7})
	got := collectAll(t, kitsune.Window(p, 3))
	if len(got) != 3 {
		t.Fatalf("expected 3 windows, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2, 3}) {
		t.Errorf("window 0: %v", got[0])
	}
	if !sliceEqual(got[1], []int{4, 5, 6}) {
		t.Errorf("window 1: %v", got[1])
	}
	if !sliceEqual(got[2], []int{7}) {
		t.Errorf("window 2 (partial): %v", got[2])
	}
}

func TestSlidingWindow(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.SlidingWindow(p, 3, 1))
	if len(got) != 3 {
		t.Fatalf("expected 3 windows, got %d: %v", len(got), got)
	}
	if !sliceEqual(got[0], []int{1, 2, 3}) {
		t.Errorf("window 0: %v", got[0])
	}
	if !sliceEqual(got[1], []int{2, 3, 4}) {
		t.Errorf("window 1: %v", got[1])
	}
	if !sliceEqual(got[2], []int{3, 4, 5}) {
		t.Errorf("window 2: %v", got[2])
	}
}

// ---------------------------------------------------------------------------
// Scan / Reduce
// ---------------------------------------------------------------------------

func TestScan(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4})
	got := collectAll(t, kitsune.Scan(p, 0, func(acc, v int) int { return acc + v }))
	want := []int{1, 3, 6, 10}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReduce(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got := collectAll(t, kitsune.Reduce(p, 0, func(acc, v int) int { return acc + v }))
	want := []int{15}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReduceEmpty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got := collectAll(t, kitsune.Reduce(p, 42, func(acc, v int) int { return acc + v }))
	want := []int{42}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Distinct / DistinctBy / Dedupe / DedupeBy
// ---------------------------------------------------------------------------

func TestDistinct(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 1, 3, 2, 4})
	got := collectAll(t, kitsune.Distinct(p))
	want := []int{1, 2, 3, 4}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDedupe(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 1, 2, 2, 3, 1, 1})
	got := collectAll(t, kitsune.Dedupe(p))
	want := []int{1, 2, 3, 1}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// GroupBy / Frequencies
// ---------------------------------------------------------------------------

func TestGroupBy(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	byKey, err := kitsune.GroupBy(ctx, p, func(s string) string { return s })
	if err != nil {
		t.Fatal(err)
	}
	if len(byKey["a"]) != 3 || len(byKey["b"]) != 2 || len(byKey["c"]) != 1 {
		t.Fatalf("unexpected groups: %v", byKey)
	}
}

func TestFrequencies(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "a", "c", "b", "a"})
	freq, err := kitsune.Frequencies(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if freq["a"] != 3 || freq["b"] != 2 || freq["c"] != 1 {
		t.Fatalf("unexpected frequencies: %v", freq)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMerge(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{4, 5, 6})
	got := collectAll(t, kitsune.Merge(a, b))
	sort.Ints(got)
	want := []int{1, 2, 3, 4, 5, 6}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Partition
// ---------------------------------------------------------------------------

func TestPartition(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	evens, odds := kitsune.Partition(p, func(v int) bool { return v%2 == 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var evenItems, oddItems []int
	evenRunner := evens.ForEach(func(_ context.Context, v int) error {
		evenItems = append(evenItems, v)
		return nil
	}).Build()
	oddRunner := odds.ForEach(func(_ context.Context, v int) error {
		oddItems = append(oddItems, v)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(evenRunner, oddRunner)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(evenItems)
	sort.Ints(oddItems)
	if !sliceEqual(evenItems, []int{2, 4, 6}) {
		t.Fatalf("evens: got %v", evenItems)
	}
	if !sliceEqual(oddItems, []int{1, 3, 5}) {
		t.Fatalf("odds: got %v", oddItems)
	}
}

// ---------------------------------------------------------------------------
// Broadcast
// ---------------------------------------------------------------------------

func TestBroadcast(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	branches := kitsune.Broadcast(p, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got0, got1 []int
	r0 := branches[0].ForEach(func(_ context.Context, v int) error {
		got0 = append(got0, v)
		return nil
	}).Build()
	r1 := branches[1].ForEach(func(_ context.Context, v int) error {
		got1 = append(got1, v)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if !sliceEqual(got0, []int{1, 2, 3}) {
		t.Fatalf("branch 0: got %v", got0)
	}
	if !sliceEqual(got1, []int{1, 2, 3}) {
		t.Fatalf("branch 1: got %v", got1)
	}
}

// ---------------------------------------------------------------------------
// Balance
// ---------------------------------------------------------------------------

func TestBalance(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	branches := kitsune.Balance(p, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count0, count1 atomic.Int64
	r0 := branches[0].ForEach(func(_ context.Context, _ int) error {
		count0.Add(1)
		return nil
	}).Build()
	r1 := branches[1].ForEach(func(_ context.Context, _ int) error {
		count1.Add(1)
		return nil
	}).Build()

	runner, err := kitsune.MergeRunners(r0, r1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	total := count0.Load() + count1.Load()
	if total != 6 {
		t.Fatalf("expected 6 total items, got %d (0=%d, 1=%d)", total, count0.Load(), count1.Load())
	}
	// Round-robin: 3 items each
	if count0.Load() != 3 || count1.Load() != 3 {
		t.Fatalf("expected 3/3 split, got %d/%d", count0.Load(), count1.Load())
	}
}

// ---------------------------------------------------------------------------
// Zip / ZipWith
// ---------------------------------------------------------------------------

func TestZip(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"a", "b", "c"})
	got := collectAll(t, kitsune.Zip(a, b))
	want := []kitsune.Pair[int, string]{
		{First: 1, Second: "a"},
		{First: 2, Second: "b"},
		{First: 3, Second: "c"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("[%d]: got %v want %v", i, g, want[i])
		}
	}
}

func TestZipStopsOnShortInput(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	b := kitsune.FromSlice([]string{"a", "b"})
	got := collectAll(t, kitsune.Zip(a, b))
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs (b is shorter), got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// CombineLatest
// ---------------------------------------------------------------------------

func TestCombineLatest(t *testing.T) {
	a := kitsune.FromSlice([]int{1, 2})
	b := kitsune.FromSlice([]string{"x", "y", "z"})

	got := collectAll(t, kitsune.CombineLatest(a, b))
	// Must emit at least one pair combining latest from each stream.
	if len(got) == 0 {
		t.Fatal("no output from CombineLatest")
	}
}

// ---------------------------------------------------------------------------
// WithLatestFrom
// ---------------------------------------------------------------------------

func TestWithLatestFrom(t *testing.T) {
	// other emits two values; main waits 20ms so the background goroutine in
	// WithLatestFrom has time to consume at least one value from other before
	// main items arrive (WithLatestFrom drops main items until other has emitted).
	other := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		yield("v1")
		yield("v2")
		return nil
	})
	main := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		time.Sleep(20 * time.Millisecond) // let other emit first
		for _, v := range []int{1, 2, 3} {
			if !yield(v) {
				return nil
			}
		}
		return nil
	})

	got := collectAll(t, kitsune.WithLatestFrom(main, other))
	if len(got) != 3 {
		t.Fatalf("expected 3 pairs (main has 3 items), got %d: %v", len(got), got)
	}
	for _, p := range got {
		if p.Second == "" {
			t.Errorf("empty Second in pair: %v", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SlidingWindow edge cases (6d)
// ---------------------------------------------------------------------------

func TestSlidingWindowTumbling(t *testing.T) {
	// step == size → non-overlapping (tumbling) windows, same as Window.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	got, err := kitsune.Collect(ctx, kitsune.SlidingWindow(p, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2}, {3, 4}, {5, 6}}
	if len(got) != len(want) {
		t.Fatalf("got %d windows, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if !sliceEqual(got[i], w) {
			t.Errorf("window[%d]: got %v, want %v", i, got[i], w)
		}
	}
}

func TestSlidingWindowShortStream(t *testing.T) {
	// Stream shorter than window size — no windows emitted (partial windows dropped).
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2})
	got, err := kitsune.Collect(ctx, kitsune.SlidingWindow(p, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 windows for short stream, got %d: %v", len(got), got)
	}
}

func TestSlidingWindowPanics(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})

	t.Run("step zero", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step=0")
			}
		}()
		kitsune.SlidingWindow(src, 3, 0)
	})

	t.Run("step negative", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step=-1")
			}
		}()
		kitsune.SlidingWindow(src, 3, -1)
	})

	t.Run("step greater than size", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for step>size")
			}
		}()
		kitsune.SlidingWindow(src, 3, 4)
	})
}

func TestOrderedNoConcurrency(t *testing.T) {
	// Ordered() with Concurrency(1) should produce the same output as serial execution.
	items := []int{1, 2, 3, 4, 5}
	results, err := kitsune.Map(
		kitsune.FromSlice(items),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
		kitsune.Concurrency(1), kitsune.Ordered(),
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Fatalf("want %d results, got %d", len(items), len(results))
	}
	for i, v := range results {
		if want := items[i] * 2; v != want {
			t.Fatalf("results[%d] = %d, want %d", i, v, want)
		}
	}
}

func sliceEqual[T comparable](a, b []T) bool {
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
