package kitsune_test

import (
	"context"
	"flag"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// TestMain reduces the rapid iteration count under -short (task test) to keep
// the default run fast, while the full count (100) applies when run directly
// or via task test:property.
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		flag.Set("rapid.checks", "50") //nolint:errcheck
	}
	os.Exit(m.Run())
}

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

		runners := make([]kitsune.Runnable, n)
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

		runners := make([]kitsune.Runnable, n)
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

		runners := make([]kitsune.Runnable, n)
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

// ---------------------------------------------------------------------------
// Map functor laws
// ---------------------------------------------------------------------------

// TestPropMapIdentity verifies the functor identity law: mapping the identity
// function leaves the stream unchanged in value and order.
func TestPropMapIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Map(kitsune.FromSlice(in), func(_ context.Context, x int) (int, error) {
			return x, nil
		}).Collect(context.Background())
		if err != nil {
			t.Fatalf("Map(identity) error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("Map(identity) changed stream:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// TestPropMapComposition verifies the functor composition law:
// Map(Map(p, f), g) ≡ Map(p, g∘f). If stage fusion silently reorders or
// drops items, the two sides will diverge.
func TestPropMapComposition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-100, 100)).Draw(t, "in")

		f := func(_ context.Context, x int) (int, error) { return x*2 + 1, nil }
		g := func(_ context.Context, x int) (int, error) { return x*x - 3, nil }
		gof := func(_ context.Context, x int) (int, error) { v := x*2 + 1; return v*v - 3, nil }

		composed, err := kitsune.Map(kitsune.Map(kitsune.FromSlice(in), f), g).Collect(context.Background())
		if err != nil {
			t.Fatalf("Map(Map(p,f),g) error: %v", err)
		}

		fused, err := kitsune.Map(kitsune.FromSlice(in), gof).Collect(context.Background())
		if err != nil {
			t.Fatalf("Map(p,g∘f) error: %v", err)
		}

		if !slices.Equal(composed, fused) {
			t.Fatalf("Map composition law violated:\n  composed: %v\n  fused:    %v", composed, fused)
		}
	})
}

// ---------------------------------------------------------------------------
// FlatMap monad laws
// ---------------------------------------------------------------------------

// TestPropFlatMapLeftIdentity verifies the monad left identity law:
// FlatMap(FromSlice([a]), f) ≡ f(a). Wrapping a single value in a pipeline
// and FlatMapping over it must produce exactly the same items as calling f
// directly — no more, no fewer, in the same order.
func TestPropFlatMapLeftIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.IntRange(-1000, 1000).Draw(t, "a")

		// f: deterministic, non-trivial expansion so the law is meaningful.
		f := func(_ context.Context, x int, yield func(int) error) error {
			for _, v := range []int{x, x + 1, x - 1} {
				if err := yield(v); err != nil {
					return err
				}
			}
			return nil
		}

		// Left side: FlatMap(unit(a), f)
		left, err := kitsune.FlatMap(kitsune.FromSlice([]int{a}), f).Collect(context.Background())
		if err != nil {
			t.Fatalf("FlatMap(unit(a), f) error: %v", err)
		}

		// Right side: f(a) directly — the ground truth of what f produces.
		var right []int
		if err := f(context.Background(), a, func(v int) error {
			right = append(right, v)
			return nil
		}); err != nil {
			t.Fatalf("f(a) error: %v", err)
		}

		if !slices.Equal(left, right) {
			t.Fatalf("FlatMap left identity violated for a=%d:\n  left:  %v\n  right: %v", a, left, right)
		}
	})
}

// TestPropFlatMapRightIdentity verifies the monad right identity law:
// FlatMap(p, x => emit(x)) ≡ p. Wrapping each item in a single-item emission
// and unwrapping it is a no-op.
func TestPropFlatMapRightIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.FlatMap(kitsune.FromSlice(in),
			func(_ context.Context, x int, yield func(int) error) error {
				return yield(x)
			},
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("FlatMap(right-identity) error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("FlatMap right identity violated:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// TestPropFlatMapAssociativity verifies the monad associativity law (in serial
// mode where output order is deterministic):
// FlatMap(FlatMap(p, f), g) ≡ FlatMap(p, x => FlatMap(f(x), g)).
func TestPropFlatMapAssociativity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-50, 50)).Draw(t, "in")

		// f: each x expands to [x, x+1].
		f := func(_ context.Context, x int, yield func(int) error) error {
			if err := yield(x); err != nil {
				return err
			}
			return yield(x + 1)
		}
		// g: each x maps to [x*2].
		g := func(_ context.Context, x int, yield func(int) error) error {
			return yield(x * 2)
		}

		// Left side: FlatMap(FlatMap(p, f), g)
		left, err := kitsune.FlatMap(kitsune.FlatMap(kitsune.FromSlice(in), f), g).
			Collect(context.Background())
		if err != nil {
			t.Fatalf("FlatMap(FlatMap(p,f),g) error: %v", err)
		}

		// Right side: FlatMap(p, x => FlatMap(f_as_pipeline(x), g))
		right, err := kitsune.FlatMap(kitsune.FromSlice(in),
			func(ctx context.Context, x int, yield func(int) error) error {
				// Construct f(x) as a slice pipeline and compose with g inline.
				inner := kitsune.FlatMap(kitsune.FromSlice([]int{x, x + 1}), g)
				return inner.ForEach(func(_ context.Context, v int) error {
					return yield(v)
				}).Run(ctx)
			},
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("FlatMap(p, x=>FlatMap(f(x),g)) error: %v", err)
		}

		if !slices.Equal(left, right) {
			t.Fatalf("FlatMap associativity violated:\n  left:  %v\n  right: %v\n  input: %v", left, right, in)
		}
	})
}

// ---------------------------------------------------------------------------
// Scan ↔ Reduce consistency
// ---------------------------------------------------------------------------

// TestPropScanPreservesCount verifies that Scan emits exactly one value per
// input item — the running accumulation does not add or drop items.
func TestPropScanPreservesCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		count, err := kitsune.Count(context.Background(),
			kitsune.Scan(kitsune.FromSlice(in), 0, func(acc, x int) int { return acc + x }),
		)
		if err != nil {
			t.Fatalf("Count(Scan) error: %v", err)
		}

		if count != int64(len(in)) {
			t.Fatalf("Scan emitted %d items, want %d", count, len(in))
		}
	})
}

// TestPropScanLastEqualsReduce verifies that the last value emitted by Scan
// equals the single value emitted by Reduce — both compute the same total fold.
// Restricted to non-empty input because Scan on an empty stream emits nothing
// (no last value), while Reduce emits the initial value; the behaviours
// diverge by design for the empty case.
func TestPropScanLastEqualsReduce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOfN(rapid.IntRange(-1000, 1000), 1, 100).Draw(t, "in")

		fold := func(acc, x int) int { return acc + x }

		lastScan, ok, err := kitsune.Last(context.Background(),
			kitsune.Scan(kitsune.FromSlice(in), 0, fold),
		)
		if err != nil || !ok {
			t.Fatalf("Last(Scan) returned ok=%v err=%v", ok, err)
		}

		// Reduce emits one item: the final folded value.
		reduceResult, ok2, err := kitsune.First(context.Background(),
			kitsune.Reduce(kitsune.FromSlice(in), 0, fold),
		)
		if err != nil || !ok2 {
			t.Fatalf("First(Reduce) returned ok=%v err=%v", ok2, err)
		}

		if lastScan != reduceResult {
			t.Fatalf("Last(Scan) = %d, First(Reduce) = %d (input: %v)", lastScan, reduceResult, in)
		}
	})
}

// ---------------------------------------------------------------------------
// Take / Drop partition
// ---------------------------------------------------------------------------

// TestPropTakeDropPartition verifies that Take(n) and Drop(n) together cover
// the original stream exactly: their multiset union equals the input, Take
// emits the first min(n,len) items in order, and Drop emits the remainder in
// order. Uses Broadcast so both operators see an independent copy of the source.
func TestPropTakeDropPartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")
		n := rapid.IntRange(0, len(in)+5).Draw(t, "n")

		branches := kitsune.Broadcast(kitsune.FromSlice(in), 2)

		var mu sync.Mutex
		var takeItems, dropItems []int

		takeRunner := kitsune.Take(branches[0], n).ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			takeItems = append(takeItems, v)
			mu.Unlock()
			return nil
		}).Build()

		dropRunner := kitsune.Drop(branches[1], n).ForEach(func(_ context.Context, v int) error {
			mu.Lock()
			dropItems = append(dropItems, v)
			mu.Unlock()
			return nil
		}).Build()

		merged, err := kitsune.MergeRunners(takeRunner, dropRunner)
		if err != nil {
			t.Fatalf("MergeRunners error: %v", err)
		}
		if err := merged.Run(context.Background()); err != nil {
			t.Fatalf("run error: %v", err)
		}

		limit := n
		if limit > len(in) {
			limit = len(in)
		}

		// Take must emit the first min(n, len) items in input order.
		if !slices.Equal(takeItems, in[:limit]) {
			t.Fatalf("Take(%d) prefix mismatch:\n  got:  %v\n  want: %v", n, takeItems, in[:limit])
		}

		// Drop must emit the remaining items in input order.
		if !slices.Equal(dropItems, in[limit:]) {
			t.Fatalf("Drop(%d) suffix mismatch:\n  got:  %v\n  want: %v", n, dropItems, in[limit:])
		}

		// Together they reconstruct the full multiset.
		all := append(takeItems, dropItems...)
		if !sameMultiset(all, in) {
			t.Fatalf("Take+Drop multiset mismatch: got %v, want %v", all, in)
		}
	})
}

// ---------------------------------------------------------------------------
// Filter idempotency
// ---------------------------------------------------------------------------

// TestPropFilterIdempotent verifies that applying the same predicate twice
// produces the same result as applying it once. If the fast path or fusion
// applies the predicate incorrectly, a second pass will produce a different
// (smaller or different-ordered) result.
func TestPropFilterIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		isEven := func(_ context.Context, x int) (bool, error) { return x%2 == 0, nil }

		once, err := kitsune.Filter(kitsune.FromSlice(in), isEven).Collect(context.Background())
		if err != nil {
			t.Fatalf("Filter once error: %v", err)
		}

		twice, err := kitsune.Filter(kitsune.Filter(kitsune.FromSlice(in), isEven), isEven).
			Collect(context.Background())
		if err != nil {
			t.Fatalf("Filter twice error: %v", err)
		}

		if !slices.Equal(once, twice) {
			t.Fatalf("Filter not idempotent:\n  once:  %v\n  twice: %v", once, twice)
		}
	})
}

// TestPropFilterAlwaysTrueIsIdentity verifies that filtering with a predicate
// that always returns true is indistinguishable from the original stream.
func TestPropFilterAlwaysTrueIsIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Filter(kitsune.FromSlice(in),
			func(_ context.Context, _ int) (bool, error) { return true, nil },
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("Filter(always_true) error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("Filter(always_true) changed stream:\n  got:  %v\n  want: %v", got, in)
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

// ---------------------------------------------------------------------------
// Empty properties
// ---------------------------------------------------------------------------

// TestPropEmptyIsMergeIdentity verifies that Merge(Empty, p) has the same
// multiset as p alone — Empty is the identity element for Merge.
func TestPropEmptyIsMergeIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Merge(kitsune.Empty[int](), kitsune.FromSlice(in)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Merge(Empty, p) error: %v", err)
		}

		if !sameMultiset(got, in) {
			t.Fatalf("Merge(Empty, p) multiset mismatch:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// TestPropConcatEmptyLeft verifies that Concat(Empty, p) emits exactly the
// items of p in the original order — Empty is the left identity for Concat.
func TestPropConcatEmptyLeft(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Concat(
			func() *kitsune.Pipeline[int] { return kitsune.Empty[int]() },
			func() *kitsune.Pipeline[int] { return kitsune.FromSlice(in) },
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("Concat(Empty, p) error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("Concat(Empty, p) order mismatch:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// TestPropConcatEmptyRight verifies that Concat(p, Empty) emits exactly the
// items of p in the original order — Empty is the right identity for Concat.
func TestPropConcatEmptyRight(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Concat(
			func() *kitsune.Pipeline[int] { return kitsune.FromSlice(in) },
			func() *kitsune.Pipeline[int] { return kitsune.Empty[int]() },
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("Concat(p, Empty) error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("Concat(p, Empty) order mismatch:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// ---------------------------------------------------------------------------
// Never properties
// ---------------------------------------------------------------------------

// TestPropNeverAmbIdentity verifies that Amb(Never, p) forwards all items
// from p — Never is the identity element for Amb when p is non-empty.
//
// Note: Amb(Never, Empty) would deadlock because Amb determines a winner by
// the first item emitted, and neither pipeline would ever emit. This test
// constrains the input to at least one item to keep the property meaningful.
func TestPropNeverAmbIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// At least one item so Amb declares a winner and cancels Never.
		in := rapid.SliceOfN(rapid.IntRange(-1000, 1000), 1, 50).Draw(t, "in")

		got, err := kitsune.Amb(
			func() *kitsune.Pipeline[int] { return kitsune.Never[int]() },
			func() *kitsune.Pipeline[int] { return kitsune.FromSlice(in) },
		).Collect(context.Background())
		if err != nil {
			t.Fatalf("Amb(Never, p) error: %v", err)
		}

		if !sameMultiset(got, in) {
			t.Fatalf("Amb(Never, p) multiset mismatch:\n  got:  %v\n  want: %v", got, in)
		}
	})
}

// ---------------------------------------------------------------------------
// IgnoreElements properties
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SampleWith properties
// ---------------------------------------------------------------------------

// TestPropSampleWithOutputIsSubsequence verifies three invariants that hold
// regardless of scheduling:
//  1. len(output) <= len(samplerTicks): at most one emission per sampler signal.
//  2. len(output) <= len(src): consume-on-emit means each item emitted at most once.
//  3. The output is a subsequence of src (order preserved, items come from src).
func TestPropSampleWithOutputIsSubsequence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "src")
		nTicks := rapid.IntRange(0, 20).Draw(t, "nTicks")

		// Build a sampler that fires nTicks times then closes.
		ticks := make([]struct{}, nTicks)
		sampler := kitsune.FromSlice(ticks)

		// Source emits all items from src then closes.
		p := kitsune.FromSlice(src)

		got, err := kitsune.SampleWith(p, sampler).Collect(context.Background())
		if err != nil {
			t.Fatalf("SampleWith error: %v", err)
		}

		// Invariant 1: never more outputs than sampler ticks.
		if len(got) > nTicks {
			t.Fatalf("len(got)=%d > nTicks=%d", len(got), nTicks)
		}

		// Invariant 2: never more outputs than source items (consume-on-emit).
		if len(got) > len(src) {
			t.Fatalf("len(got)=%d > len(src)=%d", len(got), len(src))
		}

		// Invariant 3: output is a subsequence of src.
		if !isSubsequence(got, src) {
			t.Fatalf("output is not a subsequence of src:\n  got: %v\n  src: %v", got, src)
		}
	})
}

// TestPropBufferWithPartitionsInput verifies three invariants for BufferWith:
//
//  1. No empty batch is ever emitted.
//  2. The flattened output is an order-preserving prefix of src. If the
//     closing selector exhausts before the source, remaining source items are
//     not read (correct: the stage exits on selector close). If the source
//     exhausts first, all items appear.
//  3. The number of output batches is at most nSignals+1.
func TestPropBufferWithPartitionsInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "src")
		nSignals := rapid.IntRange(0, 20).Draw(t, "nSignals")

		signals := make([]struct{}, nSignals)
		got, err := kitsune.BufferWith(kitsune.FromSlice(src), kitsune.FromSlice(signals)).Collect(context.Background())
		if err != nil {
			t.Fatalf("BufferWith error: %v", err)
		}

		// Invariant 1: no empty batches.
		for i, b := range got {
			if len(b) == 0 {
				t.Fatalf("batch[%d] is empty (src=%v nSignals=%d)", i, src, nSignals)
			}
		}

		// Invariant 2: flatten is an order-preserving prefix of src.
		var flat []int
		for _, b := range got {
			flat = append(flat, b...)
		}
		if len(flat) > len(src) {
			t.Fatalf("got more items than source: len(flat)=%d len(src)=%d\n  batches=%v\n  src=%v", len(flat), len(src), got, src)
		}
		for i, v := range flat {
			if src[i] != v {
				t.Fatalf("item order mismatch: flat[%d]=%d src[%d]=%d\n  batches=%v\n  src=%v", i, v, i, src[i], got, src)
			}
		}

		// Invariant 3: at most nSignals+1 batches.
		if len(got) > nSignals+1 {
			t.Fatalf("too many batches: got %d, want <= %d\n  batches=%v\n  src=%v", len(got), nSignals+1, got, src)
		}
	})
}

// isSubsequence reports whether sub is a subsequence of seq (same order, not
// necessarily contiguous).
func isSubsequence[T comparable](sub, seq []T) bool {
	si := 0
	for _, v := range seq {
		if si < len(sub) && sub[si] == v {
			si++
		}
	}
	return si == len(sub)
}

// TestPropIgnoreElementsAlwaysEmpty verifies that IgnoreElements always
// produces zero items regardless of the upstream content.
func TestPropIgnoreElementsAlwaysEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.IgnoreElements(kitsune.FromSlice(in)).Collect(context.Background())
		if err != nil {
			t.Fatalf("IgnoreElements error: %v", err)
		}

		if len(got) != 0 {
			t.Fatalf("IgnoreElements emitted %d items (want 0): %v", len(got), got)
		}
	})
}

// TestPropIgnoreElementsSideEffects verifies that IgnoreElements drains the
// upstream completely: every item passes through upstream operators (Tap, Map),
// even though none reach the downstream consumer.
func TestPropIgnoreElementsSideEffects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		var seen int
		p := kitsune.Tap(kitsune.FromSlice(in), func(_ context.Context, _ int) error {
			seen++
			return nil
		})

		got, err := kitsune.IgnoreElements(p).Collect(context.Background())
		if err != nil {
			t.Fatalf("IgnoreElements error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("IgnoreElements emitted items: %v", got)
		}
		if seen != len(in) {
			t.Fatalf("Tap ran %d times, want %d", seen, len(in))
		}
	})
}

// ---------------------------------------------------------------------------
// Materialize / Dematerialize properties
// ---------------------------------------------------------------------------

// TestPropMaterializeCount verifies that Materialize emits exactly len(in)+1
// notifications for any finite input: one value notification per item plus one
// terminal (complete) notification.
func TestPropMaterializeCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Materialize(kitsune.FromSlice(in)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Materialize error: %v", err)
		}

		wantLen := len(in) + 1 // items + terminal
		if len(got) != wantLen {
			t.Fatalf("Materialize produced %d notifications, want %d", len(got), wantLen)
		}
		for i, n := range got[:len(in)] {
			if !n.IsValue() {
				t.Fatalf("notification[%d]: want value, got %+v", i, n)
			}
		}
		if !got[len(in)].IsComplete() {
			t.Fatalf("last notification: want complete, got %+v", got[len(in)])
		}
	})
}

// TestPropMaterializePreservesOrder verifies that the value notifications
// produced by Materialize appear in the same order as the source items.
func TestPropMaterializePreservesOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Materialize(kitsune.FromSlice(in)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Materialize error: %v", err)
		}

		for i, v := range in {
			if got[i].Value != v {
				t.Fatalf("notification[%d]: got value %d, want %d", i, got[i].Value, v)
			}
		}
	})
}

// TestPropMaterializeDematerializeRoundtrip verifies the identity law:
// Dematerialize(Materialize(p)) ≡ p for any finite, error-free input.
func TestPropMaterializeDematerializeRoundtrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Dematerialize(kitsune.Materialize(kitsune.FromSlice(in))).
			Collect(context.Background())
		if err != nil {
			t.Fatalf("roundtrip error: %v", err)
		}

		if !slices.Equal(got, in) {
			t.Fatalf("roundtrip mismatch: got %v, want %v", got, in)
		}
	})
}

// ---------------------------------------------------------------------------
// TTLDedupSet properties
// ---------------------------------------------------------------------------

// TestPropTTLDedupSetFreshKeysPresent verifies that every key added to a
// TTLDedupSet is immediately visible via Contains before the TTL elapses.
func TestPropTTLDedupSetFreshKeysPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		keys := rapid.SliceOfN(rapid.StringN(1, 5, 10), 1, 20).Draw(t, "keys")

		set := kitsune.TTLDedupSet(5 * time.Second)
		for _, k := range keys {
			if err := set.Add(ctx, k); err != nil {
				t.Fatalf("Add(%q): %v", k, err)
			}
		}
		for _, k := range keys {
			ok, err := set.Contains(ctx, k)
			if err != nil {
				t.Fatalf("Contains(%q): %v", k, err)
			}
			if !ok {
				t.Fatalf("Contains(%q) = false immediately after Add", k)
			}
		}
	})
}

// TestPropWithKeyTTLLargeEquivalence verifies that a very large WithKeyTTL
// (effectively infinite) produces the same result as not setting the option
// at all. This is the "large-TTL equivalence" invariant: when no key ever
// expires, per-entity state accumulates identically whether or not a TTL is
// configured.
func TestPropWithKeyTTLLargeEquivalence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()

		// Draw a small set of entity keys and a sequence of key indices.
		nKeys := rapid.IntRange(1, 4).Draw(t, "nKeys")
		indices := rapid.SliceOfN(rapid.IntRange(0, nKeys-1), 1, 20).Draw(t, "indices")

		keys := make([]string, nKeys)
		for i := range keys {
			keys[i] = string(rune('a' + i))
		}
		items := make([]string, len(indices))
		for i, idx := range indices {
			items[i] = keys[idx]
		}

		stateKey := kitsune.NewKey[int]("prop-keyttl-equiv", 0)
		counterFn := func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
			n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
			if err != nil {
				return "", err
			}
			return s + ":" + string(rune('0'+n)), nil
		}

		// Without TTL.
		noTTL, err := kitsune.Collect(ctx, kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(s string) string { return s },
			stateKey,
			counterFn,
		))
		if err != nil {
			t.Fatalf("no-TTL run: %v", err)
		}

		// With a very large TTL (1 hour): should behave identically.
		withTTL, err := kitsune.Collect(ctx, kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(s string) string { return s },
			stateKey,
			counterFn,
			kitsune.WithKeyTTL(time.Hour),
		))
		if err != nil {
			t.Fatalf("with-TTL run: %v", err)
		}

		if !slices.Equal(noTTL, withTTL) {
			t.Fatalf("large-TTL broke output: without=%v with=%v", noTTL, withTTL)
		}
	})
}

// TestPropWithKeyTTLResetAfterEviction verifies the freshness invariant: when
// a key is accessed again after its TTL has elapsed, the next output starts
// from the initial value (counter resets to 1 when initial=0, increment=1).
func TestPropWithKeyTTLResetAfterEviction(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()

		// Draw the number of items before and after the sleep boundary.
		nBefore := rapid.IntRange(1, 5).Draw(t, "nBefore")
		nAfter := rapid.IntRange(1, 5).Draw(t, "nAfter")
		ttl := 5 * time.Millisecond

		// Build a slice: nBefore items "k", then nAfter items "k".
		// The sleep (TTL*3) happens inside the fn body after the last "before" item,
		// simulating the inactivity window expiring before the "after" items arrive.
		total := nBefore + nAfter
		items := make([]int, total)
		for i := range items {
			items[i] = i
		}
		stateKey := kitsune.NewKey[int]("prop-keyttl-reset", 0)

		got, err := kitsune.Collect(ctx, kitsune.MapWithKey(
			kitsune.FromSlice(items),
			func(_ int) string { return "k" },
			stateKey,
			func(ctx context.Context, ref *kitsune.Ref[int], v int) (int, error) {
				n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
				if err != nil {
					return 0, err
				}
				// v is the item index (0..total-1). Sleep after the last
				// "before" item (v == nBefore-1) to expire the key TTL before
				// the first "after" item arrives. Using v (not n) avoids
				// re-triggering the sleep after an eviction resets the counter.
				if v == nBefore-1 {
					time.Sleep(ttl * 3)
				}
				return n, nil
			},
			kitsune.WithKeyTTL(ttl),
		))
		if err != nil {
			t.Fatalf("pipeline error: %v", err)
		}
		if len(got) != total {
			t.Fatalf("expected %d results, got %d: %v", total, len(got), got)
		}
		// First nBefore outputs: 1, 2, ..., nBefore.
		for i := 0; i < nBefore; i++ {
			if got[i] != i+1 {
				t.Fatalf("before[%d]: got %d, want %d", i, got[i], i+1)
			}
		}
		// After eviction: counter restarts from 1. Only the first after-item is
		// asserted: subsequent items might trigger another TTL eviction on a
		// loaded system (which is correct WithKeyTTL behaviour), so asserting
		// a strictly incrementing sequence would be a false invariant.
		if got[nBefore] != 1 {
			t.Fatalf("after[0]: got %d, want 1 (eviction reset expected)", got[nBefore])
		}
	})
}

// TestPropTTLDedupSetDeduplication verifies the deduplication law:
// DedupeBy with a TTLDedupSet produces exactly the set of first-seen keys
// from a finite input when the TTL is long enough not to expire during the run.
func TestPropTTLDedupSetDeduplication(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		in := rapid.SliceOfN(rapid.IntRange(0, 9), 0, 20).Draw(t, "in")

		set := kitsune.TTLDedupSet(5 * time.Second)
		got, err := kitsune.Collect(ctx, kitsune.DedupeBy(
			kitsune.FromSlice(in),
			func(v int) int { return v },
			kitsune.WithDedupSet(set),
		))
		if err != nil {
			t.Fatalf("DedupeBy error: %v", err)
		}

		// Build the expected first-seen set in order.
		seen := make(map[int]bool)
		var want []int
		for _, v := range in {
			if !seen[v] {
				seen[v] = true
				want = append(want, v)
			}
		}

		if !slices.Equal(got, want) {
			t.Fatalf("deduplication mismatch: got %v, want %v", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Batch properties
// ---------------------------------------------------------------------------

// TestPropBatchPartition verifies that concatenating all batches in order
// reproduces the original input exactly: no items are added, dropped, or
// reordered.
func TestPropBatchPartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "src")
		size := rapid.IntRange(1, 10).Draw(t, "size")

		got, err := kitsune.Batch(kitsune.FromSlice(src), kitsune.BatchCount(size)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Batch error: %v", err)
		}

		var flat []int
		for _, b := range got {
			flat = append(flat, b...)
		}
		if !slices.Equal(flat, src) {
			t.Fatalf("Batch partition mismatch: flat=%v want=%v (size=%d)", flat, src, size)
		}
	})
}

// TestPropBatchSizes verifies three invariants on batch sizes:
// every batch is non-empty; all batches except possibly the last have exactly
// size items; the last batch has at most size items.
func TestPropBatchSizes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "src")
		size := rapid.IntRange(1, 10).Draw(t, "size")

		got, err := kitsune.Batch(kitsune.FromSlice(src), kitsune.BatchCount(size)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Batch error: %v", err)
		}

		if len(src) == 0 && len(got) != 0 {
			t.Fatalf("Batch(empty) produced %d batches, want 0", len(got))
		}
		for i, b := range got {
			if len(b) == 0 {
				t.Fatalf("batch[%d] is empty (src=%v size=%d)", i, src, size)
			}
			if i < len(got)-1 && len(b) != size {
				t.Fatalf("batch[%d] has len %d, want %d (not last batch; src=%v size=%d)", i, len(b), size, src, size)
			}
			if len(b) > size {
				t.Fatalf("batch[%d] has len %d > size %d (src=%v)", i, len(b), size, src)
			}
		}
	})
}

// TestPropBatchSizeOneIsIdentity verifies that Batch(p, 1) wraps each item
// in its own singleton slice, producing exactly len(src) batches.
func TestPropBatchSizeOneIsIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "src")

		got, err := kitsune.Batch(kitsune.FromSlice(src), kitsune.BatchCount(1)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Batch error: %v", err)
		}

		if len(got) != len(src) {
			t.Fatalf("Batch(p,1) produced %d batches, want %d (src=%v)", len(got), len(src), src)
		}
		for i, b := range got {
			if len(b) != 1 || b[0] != src[i] {
				t.Fatalf("batch[%d]=%v, want [%d]", i, b, src[i])
			}
		}
	})
}

// ---------------------------------------------------------------------------
// ChunkBy properties
// ---------------------------------------------------------------------------

// TestPropChunkByPartition verifies that concatenating all chunks in order
// reproduces the original input exactly.
func TestPropChunkByPartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-20, 20)).Draw(t, "src")
		mod := rapid.IntRange(1, 4).Draw(t, "mod")
		keyFn := func(v int) int { return ((v % mod) + mod) % mod }

		got, err := kitsune.ChunkBy(kitsune.FromSlice(src), keyFn).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkBy error: %v", err)
		}

		var flat []int
		for _, c := range got {
			flat = append(flat, c...)
		}
		if !slices.Equal(flat, src) {
			t.Fatalf("ChunkBy partition mismatch: flat=%v want=%v (mod=%d)", flat, src, mod)
		}
	})
}

// TestPropChunkByKeyConsistency verifies that every item within a chunk shares
// the same key, and that adjacent chunks have different keys.
func TestPropChunkByKeyConsistency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-20, 20)).Draw(t, "src")
		mod := rapid.IntRange(1, 4).Draw(t, "mod")
		keyFn := func(v int) int { return ((v % mod) + mod) % mod }

		got, err := kitsune.ChunkBy(kitsune.FromSlice(src), keyFn).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkBy error: %v", err)
		}

		if len(src) == 0 && len(got) != 0 {
			t.Fatalf("ChunkBy(empty) produced %d chunks, want 0", len(got))
		}
		for i, chunk := range got {
			if len(chunk) == 0 {
				t.Fatalf("chunk[%d] is empty (src=%v mod=%d)", i, src, mod)
			}
			k0 := keyFn(chunk[0])
			for j, v := range chunk {
				if keyFn(v) != k0 {
					t.Fatalf("chunk[%d][%d] has key %d, want %d (src=%v mod=%d)", i, j, keyFn(v), k0, src, mod)
				}
			}
			if i > 0 {
				prevKey := keyFn(got[i-1][len(got[i-1])-1])
				if prevKey == k0 {
					t.Fatalf("chunks[%d] and [%d] have same key %d but are separate chunks (src=%v mod=%d)", i-1, i, k0, src, mod)
				}
			}
		}
	})
}

// TestPropChunkByMatchesReference verifies ChunkBy against an inline reference
// implementation that groups consecutive equal-key items.
func TestPropChunkByMatchesReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-20, 20)).Draw(t, "src")
		mod := rapid.IntRange(1, 4).Draw(t, "mod")
		keyFn := func(v int) int { return ((v % mod) + mod) % mod }

		got, err := kitsune.ChunkBy(kitsune.FromSlice(src), keyFn).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkBy error: %v", err)
		}

		// Reference: linear scan grouping consecutive equal-key items.
		var want [][]int
		for _, v := range src {
			k := keyFn(v)
			if len(want) == 0 || keyFn(want[len(want)-1][0]) != k {
				want = append(want, []int{v})
			} else {
				want[len(want)-1] = append(want[len(want)-1], v)
			}
		}

		if len(got) != len(want) {
			t.Fatalf("ChunkBy chunk count: got %d, want %d (src=%v mod=%d)", len(got), len(want), src, mod)
		}
		for i := range got {
			if !slices.Equal(got[i], want[i]) {
				t.Fatalf("ChunkBy chunk[%d]: got %v, want %v (src=%v mod=%d)", i, got[i], want[i], src, mod)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// ChunkWhile properties
// ---------------------------------------------------------------------------

// TestPropChunkWhilePartition verifies that concatenating all chunks in order
// reproduces the original input exactly.
func TestPropChunkWhilePartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-10, 10)).Draw(t, "src")
		// Predicate: continue the chunk while curr >= prev (non-decreasing runs).
		pred := func(prev, curr int) bool { return curr >= prev }

		got, err := kitsune.ChunkWhile(kitsune.FromSlice(src), pred).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkWhile error: %v", err)
		}

		var flat []int
		for _, c := range got {
			flat = append(flat, c...)
		}
		if !slices.Equal(flat, src) {
			t.Fatalf("ChunkWhile partition mismatch: flat=%v want=%v", flat, src)
		}
	})
}

// TestPropChunkWhilePredConsistency verifies that the predicate holds for all
// consecutive pairs within a chunk, and is false at every chunk boundary.
func TestPropChunkWhilePredConsistency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-10, 10)).Draw(t, "src")
		pred := func(prev, curr int) bool { return curr >= prev }

		got, err := kitsune.ChunkWhile(kitsune.FromSlice(src), pred).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkWhile error: %v", err)
		}

		if len(src) == 0 && len(got) != 0 {
			t.Fatalf("ChunkWhile(empty) produced %d chunks, want 0", len(got))
		}
		for i, chunk := range got {
			if len(chunk) == 0 {
				t.Fatalf("chunk[%d] is empty (src=%v)", i, src)
			}
			// Within a chunk, every consecutive pair must satisfy pred.
			for j := 1; j < len(chunk); j++ {
				if !pred(chunk[j-1], chunk[j]) {
					t.Fatalf("chunk[%d]: pred(%d,%d)=false at position %d within chunk (src=%v)", i, chunk[j-1], chunk[j], j, src)
				}
			}
			// At the boundary between chunks, pred must be false.
			if i > 0 {
				last := got[i-1][len(got[i-1])-1]
				first := chunk[0]
				if pred(last, first) {
					t.Fatalf("boundary chunks[%d→%d]: pred(%d,%d)=true but they are separate chunks (src=%v)", i-1, i, last, first, src)
				}
			}
		}
	})
}

// TestPropChunkWhileMatchesReference verifies ChunkWhile against an inline
// reference implementation.
func TestPropChunkWhileMatchesReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-10, 10)).Draw(t, "src")
		pred := func(prev, curr int) bool { return curr >= prev }

		got, err := kitsune.ChunkWhile(kitsune.FromSlice(src), pred).Collect(context.Background())
		if err != nil {
			t.Fatalf("ChunkWhile error: %v", err)
		}

		// Reference: linear scan splitting when pred(prev, curr) is false.
		var want [][]int
		for _, v := range src {
			if len(want) == 0 || !pred(want[len(want)-1][len(want[len(want)-1])-1], v) {
				want = append(want, []int{v})
			} else {
				want[len(want)-1] = append(want[len(want)-1], v)
			}
		}

		if len(got) != len(want) {
			t.Fatalf("ChunkWhile chunk count: got %d, want %d (src=%v)", len(got), len(want), src)
		}
		for i := range got {
			if !slices.Equal(got[i], want[i]) {
				t.Fatalf("ChunkWhile chunk[%d]: got %v, want %v (src=%v)", i, got[i], want[i], src)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// SlidingWindow properties
// ---------------------------------------------------------------------------

// TestPropSlidingWindowSize verifies that every emitted window has exactly
// size items. Partial windows at the end of the stream are dropped by the
// operator, so this must hold for all emitted windows.
func TestPropSlidingWindowSize(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 10).Draw(t, "size")
		step := rapid.IntRange(1, size).Draw(t, "step")
		src := rapid.SliceOf(rapid.IntRange(-100, 100)).Draw(t, "src")

		got, err := kitsune.SlidingWindow(kitsune.FromSlice(src), size, step).Collect(context.Background())
		if err != nil {
			t.Fatalf("SlidingWindow error: %v", err)
		}

		for i, w := range got {
			if len(w) != size {
				t.Fatalf("window[%d] has len %d, want %d (size=%d step=%d src=%v)", i, len(w), size, size, step, src)
			}
		}
	})
}

// TestPropSlidingWindowCount verifies the expected number of windows:
// max(0, (len(src)-size)/step + 1). Partial windows are dropped.
func TestPropSlidingWindowCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 10).Draw(t, "size")
		step := rapid.IntRange(1, size).Draw(t, "step")
		src := rapid.SliceOf(rapid.IntRange(-100, 100)).Draw(t, "src")

		got, err := kitsune.SlidingWindow(kitsune.FromSlice(src), size, step).Collect(context.Background())
		if err != nil {
			t.Fatalf("SlidingWindow error: %v", err)
		}

		var wantCount int
		if len(src) >= size {
			wantCount = (len(src)-size)/step + 1
		}
		if len(got) != wantCount {
			t.Fatalf("SlidingWindow count: got %d windows, want %d (size=%d step=%d src=%v)", len(got), wantCount, size, step, src)
		}
	})
}

// TestPropSlidingWindowContentAndOverlap verifies two structural invariants:
// (a) each window is the correct contiguous subsequence of the source, and
// (b) adjacent windows share exactly size-step elements (the overlap invariant),
// which only applies when step < size.
func TestPropSlidingWindowContentAndOverlap(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 10).Draw(t, "size")
		step := rapid.IntRange(1, size).Draw(t, "step")
		src := rapid.SliceOf(rapid.IntRange(-100, 100)).Draw(t, "src")

		got, err := kitsune.SlidingWindow(kitsune.FromSlice(src), size, step).Collect(context.Background())
		if err != nil {
			t.Fatalf("SlidingWindow error: %v", err)
		}

		for i, w := range got {
			// (a) Content: window i == src[i*step : i*step+size].
			start := i * step
			want := src[start : start+size]
			if !slices.Equal(w, want) {
				t.Fatalf("window[%d]: got %v, want %v (size=%d step=%d src=%v)", i, w, want, size, step, src)
			}
			// (b) Overlap: w[step:] == got[i+1][:size-step], when step < size.
			if step < size && i+1 < len(got) {
				overlap := size - step
				if !slices.Equal(w[step:], got[i+1][:overlap]) {
					t.Fatalf("overlap windows[%d→%d]: tail=%v head=%v (size=%d step=%d src=%v)", i, i+1, w[step:], got[i+1][:overlap], size, step, src)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// SessionWindow properties
// ---------------------------------------------------------------------------

// TestPropSessionWindowLargeGapSingleSession verifies that when the gap is
// much larger than any realistic inter-item delay, all items from a
// synchronous source (FromSlice) land in exactly one session.
// Multi-session splitting is covered by TestPropSessionWindowMultiSession and
// TestPropSessionWindowEachItemAlone.
func TestPropSessionWindowLargeGapSingleSession(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOf(rapid.IntRange(-100, 100)).Draw(t, "src")

		// A 24-hour gap cannot fire between synchronously-delivered items.
		got, err := kitsune.SessionWindow(kitsune.FromSlice(src), 24*time.Hour).Collect(context.Background())
		if err != nil {
			t.Fatalf("SessionWindow error: %v", err)
		}

		if len(src) == 0 {
			if len(got) != 0 {
				t.Fatalf("SessionWindow(empty) produced %d sessions, want 0", len(got))
			}
			return
		}

		// Non-empty input: exactly one session containing all items.
		if len(got) != 1 {
			t.Fatalf("SessionWindow(large gap) produced %d sessions, want 1 (src=%v)", len(got), src)
		}
		if !slices.Equal(got[0], src) {
			t.Fatalf("SessionWindow session mismatch: got %v, want %v", got[0], src)
		}
	})
}

// TestPropSessionWindowMultiSession verifies the core multi-session splitting
// law: items sent in distinct batches separated by a full gap advance each land
// in their own session, in arrival order.
//
// Each property run draws 1-4 batches of 0-5 items. Empty batches are skipped
// (SessionWindow never emits an empty session). The virtual clock is advanced
// by exactly gap after each non-empty batch to trigger a flush.
func TestPropSessionWindowMultiSession(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		numBatches := rapid.IntRange(1, 4).Draw(t, "numBatches")
		batches := make([][]int, numBatches)
		for i := range batches {
			batches[i] = rapid.SliceOfN(rapid.IntRange(-100, 100), 0, 5).Draw(t, "batch")
		}

		const gap = time.Second
		clock := testkit.NewTestClock()
		ch := kitsune.NewChannel[int](32)

		sessions := make(chan []int, 64)
		done := make(chan error, 1)
		go func() {
			done <- kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
				ForEach(func(_ context.Context, s []int) error {
					sessions <- s
					return nil
				}).Run(context.Background())
		}()
		time.Sleep(pipelineStartup)

		var expected [][]int
		for _, batch := range batches {
			if len(batch) == 0 {
				continue
			}
			for _, v := range batch {
				if err := ch.Send(context.Background(), v); err != nil {
					t.Fatalf("Send error: %v", err)
				}
			}
			time.Sleep(pipelineStartup) // let pipeline consume items before advancing
			clock.Advance(gap)
			time.Sleep(pipelineStartup) // let flush propagate to sessions channel
			expected = append(expected, batch)
		}

		ch.Close()
		if err := <-done; err != nil {
			t.Fatalf("pipeline error: %v", err)
		}

		// Drain all sessions.
		close(sessions)
		var got [][]int
		for s := range sessions {
			got = append(got, s)
		}

		if len(got) != len(expected) {
			t.Fatalf("session count: got %d, want %d\n  got:  %v\n  want: %v",
				len(got), len(expected), got, expected)
		}
		for i, want := range expected {
			if !slices.Equal(got[i], want) {
				t.Fatalf("session[%d]: got %v, want %v", i, got[i], want)
			}
		}
	})
}

// TestPropSessionWindowEachItemAlone verifies that each item sent in isolation
// (with a full gap advance between each one) produces exactly one single-element
// session. This is the strongest form of the splitting law.
func TestPropSessionWindowEachItemAlone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.SliceOfN(rapid.IntRange(-100, 100), 0, 6).Draw(t, "src")

		const gap = time.Second
		clock := testkit.NewTestClock()
		ch := kitsune.NewChannel[int](32)

		sessions := make(chan []int, 64)
		done := make(chan error, 1)
		go func() {
			done <- kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
				ForEach(func(_ context.Context, s []int) error {
					sessions <- s
					return nil
				}).Run(context.Background())
		}()
		time.Sleep(pipelineStartup)

		for _, v := range src {
			if err := ch.Send(context.Background(), v); err != nil {
				t.Fatalf("Send error: %v", err)
			}
			time.Sleep(pipelineStartup)
			clock.Advance(gap)
			time.Sleep(pipelineStartup)
		}

		ch.Close()
		if err := <-done; err != nil {
			t.Fatalf("pipeline error: %v", err)
		}

		close(sessions)
		var got [][]int
		for s := range sessions {
			got = append(got, s)
		}

		if len(got) != len(src) {
			t.Fatalf("session count: got %d, want %d (src=%v got=%v)",
				len(got), len(src), src, got)
		}
		for i, v := range src {
			if len(got[i]) != 1 || got[i][0] != v {
				t.Fatalf("session[%d]: got %v, want [%d]", i, got[i], v)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// ExpandMap properties
// ---------------------------------------------------------------------------

// TestPropertyExpandMap_MaxItems_LengthBound verifies that MaxItems(k) on any
// finite tree emits exactly min(k, totalItems) items.
func TestPropertyExpandMap_MaxItems_LengthBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a flat-tree: roots expand to children, children have no
		// children of their own. This keeps the tree finite and deterministic.
		roots := rapid.SliceOfN(rapid.IntRange(100, 199), 1, 5).Draw(t, "roots")
		children := rapid.SliceOfN(rapid.IntRange(200, 299), 0, 8).Draw(t, "children")
		k := rapid.IntRange(1, 20).Draw(t, "k")

		fn := func(_ context.Context, v int) *kitsune.Pipeline[int] {
			if v >= 200 {
				return nil // children have no children
			}
			return kitsune.FromSlice(children)
		}

		ctx := context.Background()

		// Unbounded: total = len(roots) + len(roots)*len(children)
		allItems, err := kitsune.Collect(ctx, kitsune.ExpandMap(
			kitsune.FromSlice(roots), fn,
		))
		if err != nil {
			t.Fatal(err)
		}
		total := len(allItems)

		// Bounded: must get exactly min(k, total) items
		bounded, err := kitsune.Collect(ctx, kitsune.ExpandMap(
			kitsune.FromSlice(roots), fn,
			kitsune.MaxItems(k),
		))
		if err != nil {
			t.Fatal(err)
		}

		want := k
		if total < k {
			want = total
		}
		if len(bounded) != want {
			t.Fatalf("MaxItems(%d) on %d-item tree: got %d items, want %d",
				k, total, len(bounded), want)
		}
	})
}

// TestPropertyExpandMap_MaxDepth_SubsetOfUnbounded verifies that MaxDepth(d)
// never emits more items than an unbounded walk, and respects the structural
// bounds of the two-level test tree.
func TestPropertyExpandMap_MaxDepth_SubsetOfUnbounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Same flat-tree structure as above.
		roots := rapid.SliceOfN(rapid.IntRange(100, 199), 1, 5).Draw(t, "roots")
		children := rapid.SliceOfN(rapid.IntRange(200, 299), 0, 8).Draw(t, "children")
		d := rapid.IntRange(0, 5).Draw(t, "d")

		fn := func(_ context.Context, v int) *kitsune.Pipeline[int] {
			if v >= 200 {
				return nil
			}
			return kitsune.FromSlice(children)
		}

		ctx := context.Background()

		// Unbounded.
		allItems, err := kitsune.Collect(ctx, kitsune.ExpandMap(
			kitsune.FromSlice(roots), fn,
		))
		if err != nil {
			t.Fatal(err)
		}

		// Bounded by depth.
		bounded, err := kitsune.Collect(ctx, kitsune.ExpandMap(
			kitsune.FromSlice(roots), fn,
			kitsune.MaxDepth(d),
		))
		if err != nil {
			t.Fatal(err)
		}

		// Bounded must never exceed unbounded.
		if len(bounded) > len(allItems) {
			t.Fatalf("MaxDepth(%d) emitted %d items but unbounded only emitted %d",
				d, len(bounded), len(allItems))
		}

		// At depth 0: only roots emitted, no expansion.
		if d == 0 && len(bounded) != len(roots) {
			t.Fatalf("MaxDepth(0) emitted %d items, want %d (roots only)",
				len(bounded), len(roots))
		}

		// At depth >= 1 with children: all roots + children must be emitted
		// (since this is a 2-level tree: roots at depth 0, children at depth 1).
		if d >= 1 && len(bounded) != len(allItems) {
			t.Fatalf("MaxDepth(%d) on 2-level tree: got %d items, want %d",
				d, len(bounded), len(allItems))
		}
	})
}

// ---------------------------------------------------------------------------
// Pairwise properties
// ---------------------------------------------------------------------------

// TestPropPairwise verifies three invariants for Pairwise:
//
//  1. Length law: len(output) == max(0, len(input)-1).
//  2. Boundary: a stream of length 0 or 1 always produces zero pairs.
//  3. Overlap invariant: got[i].Curr == got[i+1].Prev for all adjacent pairs.
func TestPropPairwise(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")

		got, err := kitsune.Pairwise(kitsune.FromSlice(in)).Collect(context.Background())
		if err != nil {
			t.Fatalf("Pairwise error: %v", err)
		}

		// Invariant 1: length law.
		wantLen := len(in) - 1
		if wantLen < 0 {
			wantLen = 0
		}
		if len(got) != wantLen {
			t.Fatalf("Pairwise length: got %d, want %d (input len %d)", len(got), wantLen, len(in))
		}

		// Invariant 2: boundary — streams of length 0 or 1 produce no pairs.
		if len(in) <= 1 && len(got) != 0 {
			t.Fatalf("Pairwise on len=%d input produced %d pairs, want 0", len(in), len(got))
		}

		// Invariant 3: overlap — adjacent pairs share one element.
		for i := 0; i+1 < len(got); i++ {
			if got[i].Curr != got[i+1].Prev {
				t.Fatalf("overlap violated at [%d→%d]: got[%d].Curr=%v != got[%d].Prev=%v (input=%v)",
					i, i+1, i, got[i].Curr, i+1, got[i+1].Prev, in)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// MinMax properties
// ---------------------------------------------------------------------------

// TestPropMinMax verifies four invariants for MinMax:
//
//  1. Empty law: ok is false for an empty input.
//  2. Min bound: no item in the input is less than result.Min.
//  3. Max bound: no item in the input is greater than result.Max.
//  4. Membership: result.Min and result.Max both appear in the input.
func TestPropMinMax(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(-1000, 1000)).Draw(t, "in")
		less := func(a, b int) bool { return a < b }

		result, ok, err := kitsune.MinMax(context.Background(), kitsune.FromSlice(in), less)
		if err != nil {
			t.Fatalf("MinMax error: %v", err)
		}

		// Invariant 1: empty law.
		if len(in) == 0 {
			if ok {
				t.Fatal("MinMax on empty input returned ok=true")
			}
			return
		}
		if !ok {
			t.Fatal("MinMax on non-empty input returned ok=false")
		}

		// Invariant 2: Min bound — no item is less than result.Min.
		for _, v := range in {
			if less(v, result.Min) {
				t.Fatalf("item %d < Min %d (input=%v)", v, result.Min, in)
			}
		}

		// Invariant 3: Max bound — no item is greater than result.Max.
		for _, v := range in {
			if less(result.Max, v) {
				t.Fatalf("item %d > Max %d (input=%v)", v, result.Max, in)
			}
		}

		// Invariant 4: membership — Min and Max must appear in the input.
		minFound, maxFound := false, false
		for _, v := range in {
			if v == result.Min {
				minFound = true
			}
			if v == result.Max {
				maxFound = true
			}
		}
		if !minFound {
			t.Fatalf("result.Min=%d not found in input %v", result.Min, in)
		}
		if !maxFound {
			t.Fatalf("result.Max=%d not found in input %v", result.Max, in)
		}
	})
}

// ---------------------------------------------------------------------------
// GroupBy properties
// ---------------------------------------------------------------------------

// TestPropGroupByPartition verifies that GroupBy partitions the input stream
// without loss or duplication: the concatenation of all group value slices is
// multiset-equal to the original input.
func TestPropGroupByPartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 4)).Draw(t, "in")

		p := kitsune.FromSlice(in)
		result, err := kitsune.Single(
			context.Background(),
			kitsune.GroupBy(p, func(v int) int { return v % 3 }),
		)
		if err != nil {
			t.Fatalf("GroupBy error: %v", err)
		}

		// Flatten all group values.
		var got []int
		for _, items := range result {
			got = append(got, items...)
		}

		if !sameMultiset(got, in) {
			t.Fatalf("partition not complete:\n  input: %v\n  got:   %v", in, got)
		}
	})
}

// TestPropGroupByKeyOrder verifies two laws:
//  1. Key correctness: every item in group K satisfies keyFn(item) == K.
//  2. Relative ordering: items within each group appear in the same relative
//     order as they did in the original input.
func TestPropGroupByKeyOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 4)).Draw(t, "in")

		p := kitsune.FromSlice(in)
		result, err := kitsune.Single(
			context.Background(),
			kitsune.GroupBy(p, func(v int) int { return v % 3 }),
		)
		if err != nil {
			t.Fatalf("GroupBy error: %v", err)
		}

		// Law 1: key correctness.
		for k, items := range result {
			for _, item := range items {
				if item%3 != k {
					t.Fatalf("key mismatch: item %d in group %d", item, k)
				}
			}
		}

		// Law 2: relative ordering within each group must match input order.
		// Build expected per-key sequences by scanning input in order.
		expected := make(map[int][]int)
		for _, v := range in {
			k := v % 3
			expected[k] = append(expected[k], v)
		}
		for k, want := range expected {
			got := result[k]
			if len(got) != len(want) {
				t.Fatalf("group %d: len mismatch got=%d want=%d", k, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("group %d: position %d: got %d want %d\n  full group: %v\n  expected:   %v",
						k, i, got[i], want[i], got, want)
				}
			}
		}
	})
}

// TestPropGroupByGroupCount verifies:
//  1. No cross-key contamination: each key maps to exactly one entry (trivially
//     true for map[K][]T).
//  2. Group count equals the number of distinct keys in the input.
func TestPropGroupByGroupCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 4)).Draw(t, "in")

		p := kitsune.FromSlice(in)
		result, err := kitsune.Single(
			context.Background(),
			kitsune.GroupBy(p, func(v int) int { return v % 3 }),
		)
		if err != nil {
			t.Fatalf("GroupBy error: %v", err)
		}

		// Law: group count equals distinct key count.
		distinctKeys := make(map[int]struct{})
		for _, v := range in {
			distinctKeys[v%3] = struct{}{}
		}
		if len(result) != len(distinctKeys) {
			t.Fatalf("group count: got %d want %d (distinct keys in input: %v)",
				len(result), len(distinctKeys), distinctKeys)
		}
	})
}

// TestPropWithinSortInChunks verifies that when Within sorts each chunk, the
// total item count is preserved and each chunk ends up non-decreasing.
func TestPropWithinSortInChunks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := rapid.SliceOf(rapid.Int()).Draw(t, "items")
		chunkSize := rapid.IntRange(1, 10).Draw(t, "chunkSize")

		ctx := context.Background()
		got, err := kitsune.Collect(ctx,
			kitsune.Within(
				kitsune.Batch(kitsune.FromSlice(items), kitsune.BatchCount(chunkSize)),
				func(w *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
					return kitsune.Sort(w, func(a, b int) bool { return a < b })
				},
			),
		)
		if err != nil {
			t.Fatal(err)
		}

		// Flattened count equals input count.
		total := 0
		for _, chunk := range got {
			total += len(chunk)
		}
		if total != len(items) {
			t.Fatalf("total items: got %d, want %d", total, len(items))
		}

		// Each chunk is non-decreasing.
		for i, chunk := range got {
			for j := 1; j < len(chunk); j++ {
				if chunk[j-1] > chunk[j] {
					t.Fatalf("chunk %d not sorted at position %d: %v", i, j, chunk)
				}
			}
		}
	})
}

// TestPropSingle verifies the three Single laws: empty -> error, one -> value,
// more-than-one -> error.
func TestPropSingle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.Int()).Draw(t, "in")
		got, err := kitsune.Single(context.Background(), kitsune.FromSlice(in))
		switch len(in) {
		case 0:
			if err == nil {
				t.Fatalf("expected error for empty pipeline, got %d", got)
			}
		case 1:
			if err != nil {
				t.Fatalf("unexpected error for single-item pipeline: %v", err)
			}
			if got != in[0] {
				t.Fatalf("got %d, want %d", got, in[0])
			}
		default:
			if err == nil {
				t.Fatalf("expected error for %d-item pipeline, got %d", len(in), got)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Dedupe properties
// ---------------------------------------------------------------------------

// TestPropDedupe_GlobalSuppressesAllRepeats verifies that Dedupe's default
// (global, in-memory) path emits each key exactly once, in first-seen order.
func TestPropDedupe_GlobalSuppressesAllRepeats(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 9)).Draw(t, "in")

		got, err := kitsune.Collect(context.Background(),
			kitsune.Dedupe(kitsune.FromSlice(in)),
		)
		if err != nil {
			t.Fatalf("Dedupe error: %v", err)
		}

		// Build expected first-seen order.
		seen := make(map[int]bool)
		var want []int
		for _, v := range in {
			if !seen[v] {
				seen[v] = true
				want = append(want, v)
			}
		}

		if !slices.Equal(got, want) {
			t.Fatalf("global dedup: got %v, want %v (input %v)", got, want, in)
		}
	})
}

// TestPropDedupe_WindowReEmitsAfterLeave verifies that DedupeWindow(n) drops
// items whose key is in the last n emitted items and re-emits items whose
// key has left that window.
func TestPropDedupe_WindowReEmitsAfterLeave(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		in := rapid.SliceOf(rapid.IntRange(0, 5)).Draw(t, "in")

		got, err := kitsune.Collect(context.Background(),
			kitsune.Dedupe(kitsune.FromSlice(in), kitsune.DedupeWindow(n)),
		)
		if err != nil {
			t.Fatalf("Dedupe error: %v", err)
		}

		// Simulate the window semantics manually.
		var want []int
		window := make([]int, 0, n)
		inWindow := func(v int) bool {
			for _, w := range window {
				if w == v {
					return true
				}
			}
			return false
		}
		for _, v := range in {
			if inWindow(v) {
				continue
			}
			if len(window) >= n {
				window = window[1:]
			}
			window = append(window, v)
			want = append(want, v)
		}

		if !slices.Equal(got, want) {
			t.Fatalf("DedupeWindow(%d): got %v, want %v (input %v)", n, got, want, in)
		}
	})
}

// ---------------------------------------------------------------------------
// RandomSample properties
// ---------------------------------------------------------------------------

// TestPropRandomSample_Boundaries verifies that rate=0.0 emits nothing and
// rate=1.0 emits the full input in order.
func TestPropRandomSample_Boundaries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.Int()).Draw(t, "in")

		// rate=0.0 -> empty.
		none, err := kitsune.Collect(context.Background(),
			kitsune.RandomSample(kitsune.FromSlice(in), 0.0),
		)
		if err != nil {
			t.Fatalf("rate=0 error: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("rate=0.0: expected empty, got %v", none)
		}

		// rate=1.0 -> full input preserved in order.
		all, err := kitsune.Collect(context.Background(),
			kitsune.RandomSample(kitsune.FromSlice(in), 1.0),
		)
		if err != nil {
			t.Fatalf("rate=1 error: %v", err)
		}
		if !slices.Equal(all, in) {
			t.Fatalf("rate=1.0: got %v, want %v", all, in)
		}
	})
}

// ---------------------------------------------------------------------------
// Frequencies properties
// ---------------------------------------------------------------------------

// TestPropFrequenciesCountsCorrectly verifies Frequencies produces the exact
// multiset count for every input item.
func TestPropFrequenciesCountsCorrectly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(rapid.IntRange(0, 9)).Draw(t, "in")
		got, err := kitsune.Single(context.Background(),
			kitsune.Frequencies(kitsune.FromSlice(in)),
		)
		if err != nil {
			t.Fatalf("Frequencies error: %v", err)
		}
		want := make(map[int]int)
		for _, v := range in {
			want[v]++
		}
		if !maps.Equal(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// ToMap properties
// ---------------------------------------------------------------------------

// TestPropToMapLastWriterWins verifies ToMap builds the same map as a direct
// loop with last-writer-wins semantics.
func TestPropToMapLastWriterWins(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		type pair struct{ K, V int }
		ps := rapid.SliceOf(rapid.Custom(func(t *rapid.T) pair {
			return pair{
				K: rapid.IntRange(0, 5).Draw(t, "k"),
				V: rapid.Int().Draw(t, "v"),
			}
		})).Draw(t, "ps")

		got, err := kitsune.Single(context.Background(),
			kitsune.ToMap(kitsune.FromSlice(ps),
				func(p pair) int { return p.K },
				func(p pair) int { return p.V },
			),
		)
		if err != nil {
			t.Fatalf("ToMap error: %v", err)
		}
		want := make(map[int]int)
		for _, p := range ps {
			want[p.K] = p.V
		}
		if !maps.Equal(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Segment properties
// ---------------------------------------------------------------------------

// TestSegment_TransparencyProperty asserts that wrapping a Stage in a Segment
// does not change the output. For any source slice and any inner Stage that
// transforms ints to ints, the segment-wrapped pipeline must produce exactly
// the same output as the bare pipeline (segments are pure metadata).
func TestSegment_TransparencyProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.SliceOfN(rapid.IntRange(-100, 100), 0, 20).Draw(t, "input")
		// Three simple deterministic transforms parameterised by the draw.
		mode := rapid.IntRange(0, 2).Draw(t, "mode")
		addend := rapid.IntRange(-10, 10).Draw(t, "addend")

		stage := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
			return kitsune.Map(p, func(_ context.Context, v int) (int, error) {
				switch mode {
				case 0:
					return v + addend, nil
				case 1:
					return v * (addend + 1), nil
				default:
					return -v, nil
				}
			})
		})

		ctx := context.Background()
		want, err := kitsune.Collect(ctx, stage.Apply(kitsune.FromSlice(input)))
		if err != nil {
			t.Fatalf("bare stage error: %v", err)
		}
		got, err := kitsune.Collect(ctx, kitsune.NewSegment("seg", stage).Apply(kitsune.FromSlice(input)))
		if err != nil {
			t.Fatalf("segment-wrapped stage error: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("segment changed output: bare=%v segment=%v", want, got)
		}
	})
}
