//go:build property

package kitsune_test

import (
	"context"
	"slices"
	"sort"
	"sync"
	"testing"

	"pgregory.net/rapid"

	kitsune "github.com/zenbaku/go-kitsune"
)

// sameMultiset reports whether a and b contain exactly the same elements with
// the same multiplicities, regardless of order.
func sameMultiset[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[T]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Merge properties
// ---------------------------------------------------------------------------

// TestPropMergeMultiset verifies that Merge preserves the exact multiset union
// of its inputs: no items are added, dropped, or duplicated.
func TestPropMergeMultiset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(1, 5).Draw(t, "k")

		inputs := make([][]int, k)
		for i := range inputs {
			inputs[i] = rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")
		}

		pipes := make([]*kitsune.Pipeline[int], k)
		for i, s := range inputs {
			pipes[i] = kitsune.FromSlice(s)
		}

		got, err := kitsune.Merge(pipes...).Collect(context.Background())
		if err != nil {
			t.Fatalf("Merge returned error: %v", err)
		}

		var want []int
		for _, s := range inputs {
			want = append(want, s...)
		}

		if !sameMultiset(got, want) {
			t.Fatalf("Merge multiset mismatch:\n  got:  %v\n  want: %v", got, want)
		}
	})
}

// TestPropMergeLength verifies that the total item count from Merge equals the
// sum of all input lengths — a fast regression check for item loss.
func TestPropMergeLength(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(1, 6).Draw(t, "k")

		totalInput := 0
		pipes := make([]*kitsune.Pipeline[int], k)
		for i := range pipes {
			in := rapid.SliceOf(rapid.IntRange(0, 1000)).Draw(t, "in")
			totalInput += len(in)
			pipes[i] = kitsune.FromSlice(in)
		}

		got, err := kitsune.Merge(pipes...).Collect(context.Background())
		if err != nil {
			t.Fatalf("Merge returned error: %v", err)
		}

		if len(got) != totalInput {
			t.Fatalf("Merge length: got %d want %d", len(got), totalInput)
		}
	})
}

// TestPropMergeCommutativity verifies that the multiset result is identical
// regardless of the order in which pipelines are passed to Merge.
func TestPropMergeCommutativity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(2, 4).Draw(t, "k")

		inputs := make([][]int, k)
		for i := range inputs {
			inputs[i] = rapid.SliceOf(rapid.IntRange(-500, 500)).Draw(t, "in")
		}

		// Merge in original order.
		pipes1 := make([]*kitsune.Pipeline[int], k)
		for i, s := range inputs {
			pipes1[i] = kitsune.FromSlice(s)
		}
		got1, err := kitsune.Merge(pipes1...).Collect(context.Background())
		if err != nil {
			t.Fatalf("Merge (original order) error: %v", err)
		}

		// Merge in reversed order.
		pipes2 := make([]*kitsune.Pipeline[int], k)
		for i, s := range inputs {
			pipes2[k-1-i] = kitsune.FromSlice(s)
		}
		got2, err := kitsune.Merge(pipes2...).Collect(context.Background())
		if err != nil {
			t.Fatalf("Merge (reversed order) error: %v", err)
		}

		if !sameMultiset(got1, got2) {
			t.Fatalf("Merge not commutative:\n  forward: %v\n  reversed: %v", got1, got2)
		}
	})
}

// ---------------------------------------------------------------------------
// Sort properties
// ---------------------------------------------------------------------------

// TestPropSortIsSorted verifies that Sort always produces output in sorted order.
func TestPropSortIsSorted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		less := func(a, b int) bool { return a < b }
		got, err := kitsune.Sort(kitsune.FromSlice(in), less).Collect(context.Background())
		if err != nil {
			t.Fatalf("Sort error: %v", err)
		}

		if !slices.IsSorted(got) {
			t.Fatalf("Sort output not sorted: %v", got)
		}
	})
}

// TestPropSortPreservesMultiset verifies that Sort does not add or drop items.
func TestPropSortPreservesMultiset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		less := func(a, b int) bool { return a < b }
		got, err := kitsune.Sort(kitsune.FromSlice(in), less).Collect(context.Background())
		if err != nil {
			t.Fatalf("Sort error: %v", err)
		}

		if !sameMultiset(got, in) {
			t.Fatalf("Sort changed multiset:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// TestPropSortIdempotent verifies that Sort(Sort(p)) == Sort(p).
func TestPropSortIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-500, 500)).Draw(t, "in")
		less := func(a, b int) bool { return a < b }

		got1, err := kitsune.Sort(kitsune.FromSlice(in), less).Collect(context.Background())
		if err != nil {
			t.Fatalf("Sort once error: %v", err)
		}

		got2, err := kitsune.Sort(
			kitsune.Sort(kitsune.FromSlice(in), less),
			less,
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("Sort twice error: %v", err)
		}

		if !slices.Equal(got1, got2) {
			t.Fatalf("Sort not idempotent:\n  once:  %v\n  twice: %v", got1, got2)
		}
	})
}

// ---------------------------------------------------------------------------
// Take + Sort properties
// ---------------------------------------------------------------------------

// TestPropTakeAfterSort verifies the roadmap invariant: Take(n) after Sort
// produces exactly the n smallest elements. That is, Take(n, Sort(p)) equals
// the first n elements of a sorted copy of the input.
func TestPropTakeAfterSort(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-500, 500)).Draw(t, "in")
		n := rapid.IntRange(0, len(in)+5).Draw(t, "n")

		less := func(a, b int) bool { return a < b }
		sorted := kitsune.Sort(kitsune.FromSlice(in), less)
		got, err := kitsune.Take(sorted, n).Collect(context.Background())
		if err != nil {
			t.Fatalf("Take∘Sort error: %v", err)
		}

		// Reference: sort a copy of the input, take min(n, len) items.
		ref := make([]int, len(in))
		copy(ref, in)
		sort.Ints(ref)
		limit := n
		if limit > len(ref) {
			limit = len(ref)
		}
		want := ref[:limit]

		if !slices.Equal(got, want) {
			t.Fatalf("Take(%d)∘Sort:\n  got:  %v\n  want: %v\n  input: %v", n, got, want, in)
		}
	})
}

// TestPropTakeBounded verifies that Take(n) always emits exactly min(n, len(p)) items.
func TestPropTakeBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 1000)).Draw(t, "in")
		n := rapid.IntRange(0, len(in)+10).Draw(t, "n")

		got, err := kitsune.Take(kitsune.FromSlice(in), n).Collect(context.Background())
		if err != nil {
			t.Fatalf("Take error: %v", err)
		}

		expected := n
		if expected > len(in) {
			expected = len(in)
		}
		if len(got) != expected {
			t.Fatalf("Take(%d) of %d-item stream: got %d items", n, len(in), len(got))
		}
	})
}

// TestPropTakePreservesOrder verifies that Take emits a prefix of the input
// in the original order (no reordering).
func TestPropTakePreservesOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 1000)).Draw(t, "in")
		n := rapid.IntRange(0, len(in)+5).Draw(t, "n")

		got, err := kitsune.Take(kitsune.FromSlice(in), n).Collect(context.Background())
		if err != nil {
			t.Fatalf("Take error: %v", err)
		}

		limit := n
		if limit > len(in) {
			limit = len(in)
		}
		want := in[:limit]

		if !slices.Equal(got, want) {
			t.Fatalf("Take(%d) order mismatch:\n  got:  %v\n  want: %v", n, got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Broadcast properties
// ---------------------------------------------------------------------------

// TestPropBroadcastCompleteness verifies that every branch of a Broadcast
// receives all items in the original order — no drops, no reorders, no
// cross-branch divergence.
func TestPropBroadcastCompleteness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOfN(rapid.IntRange(0, 1000), 0, 30).Draw(t, "in")
		n := rapid.IntRange(2, 5).Draw(t, "n")

		branches := kitsune.Broadcast(kitsune.FromSlice(in), n)

		results := make([][]int, n)
		var mu sync.Mutex

		runners := make([]*kitsune.Runner, n)
		for i, b := range branches {
			i := i
			runners[i] = b.ForEach(func(_ context.Context, v int) error {
				mu.Lock()
				results[i] = append(results[i], v)
				mu.Unlock()
				return nil
			}).Build()
		}

		merged, err := kitsune.MergeRunners(runners...)
		if err != nil {
			t.Fatalf("MergeRunners error: %v", err)
		}
		if err := merged.Run(context.Background()); err != nil {
			t.Fatalf("Broadcast run error: %v", err)
		}

		for i, r := range results {
			if !slices.Equal(r, in) {
				t.Fatalf("Broadcast branch %d:\n  got:  %v\n  want: %v", i, r, in)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Balance properties
// ---------------------------------------------------------------------------

// TestPropBalanceItemCount verifies that Balance preserves the multiset of
// input items across all branches (no loss, no duplication).
func TestPropBalanceItemCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOfN(rapid.IntRange(0, 10000), 0, 60).Draw(t, "in")
		n := rapid.IntRange(1, 5).Draw(t, "n")

		branches := kitsune.Balance(kitsune.FromSlice(in), n)

		results := make([][]int, n)
		var mu sync.Mutex

		runners := make([]*kitsune.Runner, n)
		for i, b := range branches {
			i := i
			runners[i] = b.ForEach(func(_ context.Context, v int) error {
				mu.Lock()
				results[i] = append(results[i], v)
				mu.Unlock()
				return nil
			}).Build()
		}

		merged, err := kitsune.MergeRunners(runners...)
		if err != nil {
			t.Fatalf("MergeRunners error: %v", err)
		}
		if err := merged.Run(context.Background()); err != nil {
			t.Fatalf("Balance run error: %v", err)
		}

		// Total count must equal input length.
		total := 0
		for _, r := range results {
			total += len(r)
		}
		if total != len(in) {
			t.Fatalf("Balance lost items: total=%d want=%d (branches: %v)", total, len(in), lengths(results))
		}

		// Multiset union of all branches must equal the input multiset.
		var merged2 []int
		for _, r := range results {
			merged2 = append(merged2, r...)
		}
		if !sameMultiset(merged2, in) {
			t.Fatalf("Balance multiset mismatch")
		}
	})
}

// TestPropBalanceRoundRobin verifies that Balance distributes items evenly:
// each branch receives either ⌊N/B⌋ or ⌈N/B⌉ items (differs by at most 1).
func TestPropBalanceRoundRobin(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOfN(rapid.IntRange(0, 100), 0, 60).Draw(t, "in")
		n := rapid.IntRange(1, 5).Draw(t, "n")

		branches := kitsune.Balance(kitsune.FromSlice(in), n)

		counts := make([]int, n)
		var mu sync.Mutex

		runners := make([]*kitsune.Runner, n)
		for i, b := range branches {
			i := i
			runners[i] = b.ForEach(func(_ context.Context, _ int) error {
				mu.Lock()
				counts[i]++
				mu.Unlock()
				return nil
			}).Build()
		}

		merged, err := kitsune.MergeRunners(runners...)
		if err != nil {
			t.Fatalf("MergeRunners error: %v", err)
		}
		if err := merged.Run(context.Background()); err != nil {
			t.Fatalf("Balance run error: %v", err)
		}

		minLen, maxLen := len(in)+1, -1
		for _, c := range counts {
			if c < minLen {
				minLen = c
			}
			if c > maxLen {
				maxLen = c
			}
		}
		if len(in) == 0 {
			return // trivially satisfied
		}
		if maxLen-minLen > 1 {
			t.Fatalf("Balance not fair: min=%d max=%d counts=%v", minLen, maxLen, counts)
		}
	})
}

// lengths returns the lengths of each slice in ss.
func lengths[T any](ss [][]T) []int {
	out := make([]int, len(ss))
	for i, s := range ss {
		out[i] = len(s)
	}
	return out
}
