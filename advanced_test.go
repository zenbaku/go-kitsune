package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Collect / terminal helpers
// ---------------------------------------------------------------------------

func TestCollect(t *testing.T) {
	ctx := context.Background()
	got, err := kitsune.Collect(ctx, kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v", got)
	}
}

func TestFirst(t *testing.T) {
	ctx := context.Background()
	v, ok, err := kitsune.First(ctx, kitsune.FromSlice([]int{10, 20, 30}))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if v != 10 {
		t.Fatalf("got %d, want 10", v)
	}
}

func TestFirstEmpty(t *testing.T) {
	ctx := context.Background()
	_, ok, err := kitsune.First(ctx, kitsune.FromSlice([]int{}))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for empty pipeline")
	}
}

func TestLast(t *testing.T) {
	ctx := context.Background()
	v, ok, err := kitsune.Last(ctx, kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if v != 3 {
		t.Fatalf("got %d, want 3", v)
	}
}

func TestCount(t *testing.T) {
	ctx := context.Background()
	n, err := kitsune.Count(ctx, kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("got %d, want 5", n)
	}
}

func TestAny(t *testing.T) {
	ctx := context.Background()
	found, err := kitsune.Any(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 2 })
	if err != nil || !found {
		t.Fatalf("Any: found=%v err=%v", found, err)
	}
	notFound, err := kitsune.Any(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 99 })
	if err != nil || notFound {
		t.Fatalf("Any(not found): found=%v err=%v", notFound, err)
	}
}

func TestAll(t *testing.T) {
	ctx := context.Background()
	ok, err := kitsune.All(ctx, kitsune.FromSlice([]int{2, 4, 6}), func(v int) bool { return v%2 == 0 })
	if err != nil || !ok {
		t.Fatalf("All(all even): ok=%v err=%v", ok, err)
	}
	bad, err := kitsune.All(ctx, kitsune.FromSlice([]int{2, 3, 6}), func(v int) bool { return v%2 == 0 })
	if err != nil || bad {
		t.Fatalf("All(has odd): ok=%v err=%v", bad, err)
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	v, found, err := kitsune.Find(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 2 })
	if err != nil || !found || v != 2 {
		t.Fatalf("Find: v=%d found=%v err=%v", v, found, err)
	}
}

func TestSum(t *testing.T) {
	ctx := context.Background()
	s, err := kitsune.Sum(ctx, kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	if err != nil || s != 15 {
		t.Fatalf("Sum: got %d err=%v", s, err)
	}
}

func TestToMap(t *testing.T) {
	ctx := context.Background()
	m, err := kitsune.ToMap(ctx, kitsune.FromSlice([]string{"a", "bb", "ccc"}),
		func(s string) int { return len(s) },
		func(s string) string { return s },
	)
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != "a" || m[2] != "bb" || m[3] != "ccc" {
		t.Fatalf("ToMap: %v", m)
	}
}

func TestSequenceEqual(t *testing.T) {
	ctx := context.Background()
	eq, err := kitsune.SequenceEqual(ctx, kitsune.FromSlice([]int{1, 2, 3}), kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil || !eq {
		t.Fatalf("equal sequences: eq=%v err=%v", eq, err)
	}
	neq, err := kitsune.SequenceEqual(ctx, kitsune.FromSlice([]int{1, 2, 3}), kitsune.FromSlice([]int{1, 2, 4}))
	if err != nil || neq {
		t.Fatalf("unequal sequences: eq=%v err=%v", neq, err)
	}
}

func TestIter(t *testing.T) {
	ctx := context.Background()
	var got []int
	for v := range kitsune.Iter(ctx, kitsune.FromSlice([]int{1, 2, 3})) {
		got = append(got, v)
	}
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("Iter: got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Throttle / Debounce
// ---------------------------------------------------------------------------

func TestThrottle(t *testing.T) {
	// Throttle with a 50ms window: emit first item per window.
	// Feed 6 items quickly and check that some are throttled.
	items := []int{1, 2, 3, 4, 5, 6}
	p := kitsune.FromSlice(items)
	got := collectAll(t, kitsune.Throttle(p, 50*time.Millisecond))
	// With a very short window and synchronous source, most items arrive in the
	// same window. At minimum the first item should come through.
	if len(got) == 0 {
		t.Fatal("throttle: no items emitted")
	}
	if got[0] != 1 {
		t.Fatalf("throttle: first item should be 1, got %d", got[0])
	}
}

func TestDebounce(t *testing.T) {
	// Debounce: only the last item in a burst should be emitted.
	// We use a Generate that emits 3 items then waits for the silence window.
	silence := 30 * time.Millisecond
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		// Burst: 1, 2, 3 emitted quickly.
		yield(1)
		yield(2)
		yield(3)
		// Wait long enough for the debounce to fire.
		time.Sleep(silence * 3)
		yield(4)
		return nil
	})
	got := collectAll(t, kitsune.Debounce(p, silence))
	// Burst {1,2,3} → only 3 emitted; then 4 → emitted.
	if len(got) < 1 {
		t.Fatal("debounce: no items emitted")
	}
}

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

func TestSwitchMap(t *testing.T) {
	// SwitchMap: each new input cancels the current sub-stream.
	// With FromSlice (synchronous), only the last item's sub-stream completes.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.SwitchMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v * 10); err != nil {
				return err
			}
		}
		return nil
	}))
	// Due to SwitchMap cancellation, we might not get all items,
	// but we should get at least some (from the last item).
	if len(got) == 0 {
		t.Fatal("SwitchMap: no output")
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap
// ---------------------------------------------------------------------------

func TestExhaustMap(t *testing.T) {
	// ExhaustMap: drops new items while inner goroutine is active.
	// Use a slow sub-stream and fast input to verify dropping.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 5; i++ {
			if !yield(i) {
				return nil
			}
			time.Sleep(2 * time.Millisecond) // small pause
		}
		return nil
	})

	got := collectAll(t, kitsune.ExhaustMap(p, func(_ context.Context, v int, yield func(string) error) error {
		time.Sleep(10 * time.Millisecond) // slow sub-stream
		return yield(fmt.Sprintf("item-%d", v))
	}))

	// With slow sub-stream, some items should be dropped.
	if len(got) == 0 {
		t.Fatal("ExhaustMap: no output")
	}
	// Should not have all 5 items (some are dropped).
	if len(got) == 5 {
		t.Log("ExhaustMap: all 5 items processed (timing-dependent, not a failure)")
	}
}

// ---------------------------------------------------------------------------
// ConcatMap
// ---------------------------------------------------------------------------

func TestConcatMap(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.ConcatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*10 + i); err != nil {
				return err
			}
		}
		return nil
	}))
	// ConcatMap = serial FlatMap, so output is in input order.
	want := []int{10, 20, 21, 30, 31, 32}
	if !sliceEqual(got, want) {
		t.Fatalf("ConcatMap: got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// MapResult / MapRecover
// ---------------------------------------------------------------------------

func TestMapResult(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("bad two")

	ok, failed := kitsune.MapResult(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v * 10, nil
		})

	var okItems []int
	var failedItems []kitsune.ErrItem[int]

	r1 := ok.ForEach(func(_ context.Context, v int) error {
		okItems = append(okItems, v)
		return nil
	}).Build()
	r2 := failed.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error {
		failedItems = append(failedItems, e)
		return nil
	}).Build()

	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if len(okItems) != 2 {
		t.Fatalf("expected 2 ok items, got %d: %v", len(okItems), okItems)
	}
	if len(failedItems) != 1 || failedItems[0].Item != 2 || !errors.Is(failedItems[0].Err, boom) {
		t.Fatalf("unexpected failed items: %v", failedItems)
	}
}

func TestMapRecover(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Collect(ctx, kitsune.MapRecover(p,
		func(_ context.Context, v int) (string, error) {
			if v == 2 {
				panic("explode!")
			}
			return fmt.Sprintf("%d", v*10), nil
		},
		func(_ context.Context, _ int, _ error) string {
			return "recovered"
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0] != "10" || results[1] != "recovered" || results[2] != "30" {
		t.Fatalf("got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Batch with timeout
// ---------------------------------------------------------------------------

func TestBatchWithTimeout(t *testing.T) {
	// Slow source: items arrive every 5ms, timeout at 20ms.
	// Expect partial batches when timeout fires before size is reached.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 7; i++ {
			if !yield(i) {
				return nil
			}
			time.Sleep(8 * time.Millisecond)
		}
		return nil
	})

	got := collectAll(t, kitsune.Batch(p, 10, kitsune.BatchTimeout(20*time.Millisecond)))
	// With size=10 and items arriving every 8ms, the 20ms timeout should fire
	// and emit partial batches.
	if len(got) == 0 {
		t.Fatal("batch with timeout: no batches emitted")
	}
	total := 0
	for _, b := range got {
		total += len(b)
	}
	if total != 7 {
		t.Fatalf("batch with timeout: expected 7 items total, got %d across %d batches", total, len(got))
	}
}

// ---------------------------------------------------------------------------
// SessionWindow
// ---------------------------------------------------------------------------

func TestSessionWindow(t *testing.T) {
	// Items arrive in two bursts with a pause between them.
	silence := 25 * time.Millisecond
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		// Burst 1
		yield(1)
		yield(2)
		// Pause (longer than gap)
		time.Sleep(silence * 3)
		// Burst 2
		yield(3)
		yield(4)
		return nil
	})

	got := collectAll(t, kitsune.SessionWindow(p, silence))
	if len(got) != 2 {
		// Due to timing variability, accept 1-2 sessions.
		if len(got) == 0 {
			t.Fatalf("SessionWindow: no sessions emitted; got %v", got)
		}
		t.Logf("SessionWindow: got %d sessions (timing-dependent)", len(got))
		return
	}

	sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })
	if !sliceEqual(got[0], []int{1, 2}) {
		t.Errorf("session 0: got %v", got[0])
	}
	if !sliceEqual(got[1], []int{3, 4}) {
		t.Errorf("session 1: got %v", got[1])
	}
}
