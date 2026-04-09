package kitsune_test

// Regression tests that expose known bugs in the current implementation.
// Each test is expected to FAIL until the corresponding fix is applied.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// BUG 1: SequenceEqual returns true for equal-prefix sequences of different length
//
// Root cause: SequenceEqual uses Zip, which stops at the shorter sequence.
// If [1,2,3] and [1,2,3,4] are zipped, only 3 pairs are produced and all
// match, so the result is true. The extra item 4 is never observed.
// ---------------------------------------------------------------------------

func TestSequenceEqualDifferentLengthReturnsFalse(t *testing.T) {
	ctx := context.Background()

	// Shorter is the left argument.
	eq, err := kitsune.SequenceEqual(ctx,
		kitsune.FromSlice([]int{1, 2, 3}),
		kitsune.FromSlice([]int{1, 2, 3, 4}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("SequenceEqual([1,2,3], [1,2,3,4]) returned true; want false")
	}
}

func TestSequenceEqualDifferentLengthReturnsFalseReversed(t *testing.T) {
	ctx := context.Background()

	// Shorter is the right argument.
	eq, err := kitsune.SequenceEqual(ctx,
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		kitsune.FromSlice([]int{1, 2, 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("SequenceEqual([1,2,3,4], [1,2,3]) returned true; want false")
	}
}

// ---------------------------------------------------------------------------
// BUG 2: StartWith uses Merge instead of Concat
//
// Root cause: Merge is unordered — both the prefix source and the main
// source goroutine run concurrently, so items from the main source can
// arrive before all prefix items have been emitted.
//
// This test asserts a strict ordering guarantee (prefix always first) which
// StartWith must provide. It runs the scenario many times to expose the
// non-determinism introduced by Merge.
// ---------------------------------------------------------------------------

func TestStartWithPrefixAlwaysFirst(t *testing.T) {
	ctx := context.Background()

	const attempts = 200
	for i := 0; i < attempts; i++ {
		// p emits a large slice so its goroutine stays busy while prefix fires.
		p := kitsune.FromSlice(makeInts(100))
		result := kitsune.StartWith(p, -1, -2, -3)

		got, err := kitsune.Collect(ctx, result)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if len(got) != 103 {
			t.Fatalf("attempt %d: got %d items, want 103", i, len(got))
		}
		// The three prefix items must be first, in order.
		if got[0] != -1 || got[1] != -2 || got[2] != -3 {
			t.Fatalf("attempt %d: prefix items not first; got[0:3] = %v", i, got[:3])
		}
	}
}

// makeInts returns []int{0, 1, ..., n-1}.
func makeInts(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

// ---------------------------------------------------------------------------
// BUG 3: ExhaustMap silently drops errors from inner goroutines
//
// Root cause: the inner goroutine uses `_, _ = ProcessFlatMapItem(...)`.
// Errors are never propagated to the outer stage, so Run() returns nil
// even when every fn invocation fails.
// ---------------------------------------------------------------------------

var errExhaustInner = errors.New("inner failure")

func TestExhaustMapPropagatesInnerError(t *testing.T) {
	ctx := context.Background()

	p := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.ExhaustMap(p, func(_ context.Context, v int, yield func(string) error) error {
		return errExhaustInner
	})

	_, err := kitsune.Collect(ctx, out)
	if err == nil {
		t.Fatal("ExhaustMap swallowed inner error; Run() returned nil, want errExhaustInner")
	}
	if !errors.Is(err, errExhaustInner) {
		t.Fatalf("ExhaustMap: got error %v, want to wrap errExhaustInner", err)
	}
}

// ---------------------------------------------------------------------------
// BUG 4: combineStageLists mutated the source stageList (fixed in P2)
//
// Root cause: combineStageLists was appending stages from secondary stageLists
// into the primary stageList in-place. Building a second Merge from the same
// shared source contaminated the first Merge's stageList: it ran competing
// readers on shared.ch, and unread output channels of the second Merge would
// fill under backpressure and deadlock.
//
// Fix: combineStageLists now creates a new stageList; inputs are never mutated.
// ---------------------------------------------------------------------------

func TestMergeDoesNotCrossContaminateStages(t *testing.T) {
	ctx := context.Background()

	// Build two independent Merges that share a common source pipeline.
	shared := kitsune.FromSlice([]int{10, 20, 30})
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]int{4, 5, 6})

	x := kitsune.Merge(shared, a)

	// Before the fix, building y mutated shared.sl, contaminating x's stageList
	// with y's stages. Running x would then also run y's merge goroutines, which
	// competed for shared.ch items and could deadlock on y's unread output channel.
	_ = kitsune.Merge(shared, b) // intentionally not run

	// Run only x; should get exactly 6 items from shared+a, deterministically.
	got, err := kitsune.Collect(ctx, x)
	if err != nil {
		t.Fatalf("Merge cross-contamination: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("Merge cross-contamination: got %d items %v, want 6 (shared+a)", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Merge / Broadcast edge cases
// ---------------------------------------------------------------------------

func TestMergeErrorPropagation(t *testing.T) {
	// Merge must surface errors (e.g. context cancellation) rather than
	// silently swallowing them.
	ctx, cancel := context.WithCancel(context.Background())

	p := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})

	outs := kitsune.Broadcast(p, 2)
	merged := kitsune.Merge(outs[0], outs[1])

	done := make(chan error, 1)
	go func() {
		_, err := merged.Collect(ctx)
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error after context cancellation, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not terminate after context cancellation")
	}
}

func TestBroadcastPanics(t *testing.T) {
	t.Run("n=1", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for Broadcast(p, 1)")
			}
		}()
		kitsune.Broadcast(kitsune.FromSlice([]int{1, 2, 3}), 1)
	})

	t.Run("n=0", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for Broadcast(p, 0)")
			}
		}()
		kitsune.Broadcast(kitsune.FromSlice([]int{1, 2, 3}), 0)
	})
}

func TestMergeEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("no pipelines returns empty", func(t *testing.T) {
		// Current behaviour: Merge() with no args returns an empty pipeline.
		got, err := kitsune.Merge[int]().Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty result from Merge(), got %v", got)
		}
	})

	t.Run("two independent graphs", func(t *testing.T) {
		p1 := kitsune.FromSlice([]int{1})
		p2 := kitsune.FromSlice([]int{2})
		got, err := kitsune.Merge(p1, p2).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		if !slices.Equal(got, []int{1, 2}) {
			t.Errorf("got %v, want [1 2]", got)
		}
	})

	t.Run("single pipeline passthrough", func(t *testing.T) {
		got, err := kitsune.Merge(kitsune.FromSlice([]int{1, 2, 3})).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []int{1, 2, 3}) {
			t.Fatalf("expected [1 2 3], got %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Independent-graph multi-stream (6f)
// ---------------------------------------------------------------------------

func TestMerge_Independent_ErrorPropagates(t *testing.T) {
	boom := errors.New("stage error")
	good := kitsune.FromSlice([]int{1, 2, 3})
	bad := kitsune.Map(kitsune.FromSlice([]int{4, 5}),
		func(_ context.Context, v int) (int, error) {
			if v == 5 {
				return 0, boom
			}
			return v, nil
		},
	)

	_, err := kitsune.Merge(good, bad).Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom to propagate, got %v", err)
	}
}

func TestMerge_Independent_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	infinite := func() *kitsune.Pipeline[int] {
		return kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
			for i := 0; ; i++ {
				if !yield(i) {
					return nil
				}
			}
		})
	}

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Merge(infinite(), infinite()).Collect(ctx)
		done <- err
	}()

	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error after context cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not terminate after context cancel")
	}
}

func TestMerge_Independent_TakeDownstream(t *testing.T) {
	ctx := context.Background()

	a := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})
	b := kitsune.Generate(func(_ context.Context, yield func(int) bool) error {
		for i := 100; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})

	got, err := kitsune.Merge(a, b).Take(3).Collect(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d: %v", len(got), got)
	}
}

func TestZip_IndependentGraphs(t *testing.T) {
	ctx := context.Background()
	a := kitsune.FromSlice([]int{1, 2, 3})
	b := kitsune.FromSlice([]string{"x", "y", "z"})

	got, err := kitsune.Zip(a, b).Collect(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []kitsune.Pair[int, string]{{First: 1, Second: "x"}, {First: 2, Second: "y"}, {First: 3, Second: "z"}}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d: %v", len(got), len(want), got)
	}
	for i, p := range got {
		if p != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, p, want[i])
		}
	}
}

func TestWithLatestFrom_IndependentGraphs(t *testing.T) {
	// other emits values; main waits so other has emitted before main items arrive.
	other := kitsune.Generate(func(_ context.Context, yield func(string) bool) error {
		yield("latest")
		return nil
	})
	main := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		time.Sleep(20 * time.Millisecond) // let other emit first
		for _, v := range []int{1, 2} {
			if !yield(v) {
				return nil
			}
		}
		return nil
	})

	got, err := kitsune.WithLatestFrom(main, other).Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs (main has 2 items), got %d: %v", len(got), got)
	}
	for _, p := range got {
		if p.Second != "latest" {
			t.Errorf("expected Second='latest', got %q", p.Second)
		}
	}
}

func TestCombineLatest_IndependentGraphs(t *testing.T) {
	ctx := context.Background()
	a := kitsune.FromSlice([]int{1, 2})
	b := kitsune.FromSlice([]string{"x", "y"})

	got, err := kitsune.CombineLatest(a, b).Collect(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one pair from CombineLatest")
	}
}
