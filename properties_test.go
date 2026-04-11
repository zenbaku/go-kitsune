package kitsune_test

import (
	"context"
	"flag"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"

	"pgregory.net/rapid"

	kitsune "github.com/zenbaku/go-kitsune"
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
