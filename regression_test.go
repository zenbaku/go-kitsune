package kitsune_test

// Regression tests that expose known bugs in the current implementation.
// Each test is expected to FAIL until the corresponding fix is applied.

import (
	"context"
	"errors"
	"testing"

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
