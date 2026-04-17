package kitsune_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// MapIntersperse
// ---------------------------------------------------------------------------

func TestMapIntersperse(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	got, err := kitsune.Collect(ctx, kitsune.MapIntersperse(p, ",",
		func(_ context.Context, s string) (string, error) { return strings.ToUpper(s), nil },
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", ",", "B", ",", "C"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMapIntersperseSingle(t *testing.T) {
	ctx := context.Background()
	got, err := kitsune.Collect(ctx, kitsune.MapIntersperse(kitsune.FromSlice([]int{42}), 0,
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 84 {
		t.Fatalf("got %v, want [84]", got)
	}
}

// ---------------------------------------------------------------------------
// CountBy
// ---------------------------------------------------------------------------

func TestCountBy(t *testing.T) {
	ctx := context.Background()
	items := []string{"a", "b", "a", "c", "b", "a"}
	p := kitsune.FromSlice(items)
	snapshots, err := kitsune.Collect(ctx, kitsune.CountBy(p, func(s string) string { return s }))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != len(items) {
		t.Fatalf("expected %d snapshots, got %d", len(items), len(snapshots))
	}
	last := snapshots[len(snapshots)-1]
	if last["a"] != 3 || last["b"] != 2 || last["c"] != 1 {
		t.Fatalf("final snapshot: %v", last)
	}
}

// ---------------------------------------------------------------------------
// SumBy
// ---------------------------------------------------------------------------

func TestSumBy(t *testing.T) {
	ctx := context.Background()
	type txn struct {
		account string
		amount  float64
	}
	items := []txn{
		{"alice", 10}, {"bob", 5}, {"alice", 20}, {"bob", 15},
	}
	snapshots, err := kitsune.Collect(ctx, kitsune.SumBy(kitsune.FromSlice(items),
		func(t txn) string { return t.account },
		func(t txn) float64 { return t.amount },
	))
	if err != nil {
		t.Fatal(err)
	}
	last := snapshots[len(snapshots)-1]
	if last["alice"] != 30 || last["bob"] != 20 {
		t.Fatalf("final snapshot: %v", last)
	}
}

// ---------------------------------------------------------------------------
// MapBatch
// ---------------------------------------------------------------------------

func TestMapBatch(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	var batchSizes []int
	got, err := kitsune.Collect(ctx, kitsune.MapBatch(p, 2,
		func(_ context.Context, batch []int) ([]int, error) {
			batchSizes = append(batchSizes, len(batch))
			out := make([]int, len(batch))
			for i, v := range batch {
				out[i] = v * 10
			}
			return out, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	want := []int{10, 20, 30, 40, 50}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMapBatchError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("batch failed")
	p := kitsune.FromSlice([]int{1, 2, 3})
	_, err := kitsune.Collect(ctx, kitsune.MapBatch(p, 3,
		func(_ context.Context, _ []int) ([]int, error) { return nil, boom },
	))
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stage.Or
// ---------------------------------------------------------------------------

func TestStageOr(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("primary failed")

	primary := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, v int) (string, error) {
			if v == 2 {
				return "", boom
			}
			return "primary", nil
		})
	})
	fallback := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, _ int) (string, error) {
			return "fallback", nil
		})
	})

	got, err := kitsune.Collect(ctx, primary.Or(fallback)(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %v", got)
	}
	if got[0] != "primary" || got[1] != "fallback" || got[2] != "primary" {
		t.Fatalf("got %v", got)
	}
}

func TestOr_ComposesWithThen(t *testing.T) {
	ctx := context.Background()

	primary := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})
	fallback := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n, nil })
	})
	doubleAgain := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	// primary always succeeds → Or never triggers fallback.
	// Then composes: (n*2) * 2 = n*4
	composed := kitsune.Then(primary.Or(fallback), doubleAgain)
	results, err := composed.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0] != 4 || results[1] != 8 || results[2] != 12 {
		t.Errorf("got %v, want [4 8 12]", results)
	}
}

// ---------------------------------------------------------------------------
// Or (free function)
// ---------------------------------------------------------------------------

func TestOr_PrimarySucceeds(t *testing.T) {
	ctx := context.Background()
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "primary", nil },
		func(_ context.Context, n int) (string, error) { return "fallback", nil },
	)
	results, err := stage.Apply(kitsune.FromSlice([]int{1, 2, 3})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r != "primary" {
			t.Errorf("expected primary result, got %q", r)
		}
	}
}

func TestOr_FallbackOnPrimaryError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "", boom },
		func(_ context.Context, n int) (string, error) { return "fallback", nil },
	)
	results, err := stage.Apply(kitsune.FromSlice([]int{1})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != "fallback" {
		t.Errorf("got %v, want [fallback]", results)
	}
}

func TestOr_BothFail(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "", boom },
		func(_ context.Context, n int) (string, error) { return "", boom },
	)
	_, err := stage.Apply(kitsune.FromSlice([]int{1})).Collect(ctx)
	if !errors.Is(err, boom) {
		t.Errorf("expected boom, got %v", err)
	}
}

func TestOr_BothFailJoinsDistinctErrors(t *testing.T) {
	ctx := context.Background()
	errPrimary := errors.New("primary failed")
	errFallback := errors.New("fallback failed")
	stage := kitsune.Or(
		func(_ context.Context, n int) (string, error) { return "", errPrimary },
		func(_ context.Context, n int) (string, error) { return "", errFallback },
	)
	_, err := stage.Apply(kitsune.FromSlice([]int{1})).Collect(ctx)
	if !errors.Is(err, errPrimary) {
		t.Errorf("expected primary error in joined error, got %v", err)
	}
	if !errors.Is(err, errFallback) {
		t.Errorf("expected fallback error in joined error, got %v", err)
	}
}

func TestStageOr_BothFailJoinsErrors(t *testing.T) {
	ctx := context.Background()
	errPrimary := errors.New("primary failed")
	errFallback := errors.New("fallback failed")

	primary := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, _ int) (string, error) {
			return "", errPrimary
		})
	})
	fallback := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, _ int) (string, error) {
			return "", errFallback
		})
	})

	_, err := primary.Or(fallback).Apply(kitsune.FromSlice([]int{1})).Collect(ctx)
	if !errors.Is(err, errPrimary) {
		t.Errorf("expected primary error in joined error, got %v", err)
	}
	if !errors.Is(err, errFallback) {
		t.Errorf("expected fallback error in joined error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CountBy edge cases (6e)
// ---------------------------------------------------------------------------

func TestCountBy_EmptyStream(t *testing.T) {
	ctx := context.Background()
	snapshots, err := kitsune.Collect(ctx, kitsune.CountBy(kitsune.FromSlice([]string{}),
		func(s string) string { return s },
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Errorf("expected 0 snapshots, got %d: %v", len(snapshots), snapshots)
	}
}

func TestCountBy_SnapshotIsolation(t *testing.T) {
	// Mutating a returned snapshot must not affect the next one.
	ctx := context.Background()
	snapshots, err := kitsune.Collect(ctx, kitsune.CountBy(
		kitsune.FromSlice([]string{"a", "a"}),
		func(s string) string { return s },
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("expected at least 2 snapshots, got %d", len(snapshots))
	}
	// Mutate the first snapshot.
	snapshots[0]["a"] = 999
	// The second snapshot should be unaffected.
	if snapshots[1]["a"] != 2 {
		t.Errorf("snapshot isolation broken: snapshot[1][a]=%d, want 2", snapshots[1]["a"])
	}
}

func TestCountBy_MultipleInstances(t *testing.T) {
	// Two CountBy stages on independent pipelines must not share state.
	ctx := context.Background()

	p1 := kitsune.FromSlice([]string{"x", "x", "x"})
	p2 := kitsune.FromSlice([]string{"y", "y"})

	s1, err := kitsune.Collect(ctx, kitsune.CountBy(p1, func(s string) string { return s }))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := kitsune.Collect(ctx, kitsune.CountBy(p2, func(s string) string { return s }))
	if err != nil {
		t.Fatal(err)
	}

	last1 := s1[len(s1)-1]
	last2 := s2[len(s2)-1]
	if last1["x"] != 3 {
		t.Errorf("p1 final count: x=%d, want 3", last1["x"])
	}
	if last2["y"] != 2 {
		t.Errorf("p2 final count: y=%d, want 2", last2["y"])
	}
	// p2 must not see p1's keys
	if _, ok := last2["x"]; ok {
		t.Error("p2 snapshot contains p1's key 'x' (state shared)")
	}
}

// ---------------------------------------------------------------------------
// SumBy edge cases (6e)
// ---------------------------------------------------------------------------

func TestSumBy_EmptyStream(t *testing.T) {
	ctx := context.Background()
	type item struct {
		k string
		v int
	}
	snapshots, err := kitsune.Collect(ctx, kitsune.SumBy(
		kitsune.FromSlice([]item{}),
		func(i item) string { return i.k },
		func(i item) int { return i.v },
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(snapshots))
	}
}

func TestSumBy_SnapshotIsolation(t *testing.T) {
	type item struct {
		k string
		v int
	}
	ctx := context.Background()
	snapshots, err := kitsune.Collect(ctx, kitsune.SumBy(
		kitsune.FromSlice([]item{{"a", 10}, {"a", 20}}),
		func(i item) string { return i.k },
		func(i item) int { return i.v },
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("expected at least 2 snapshots, got %d", len(snapshots))
	}
	snapshots[0]["a"] = 9999
	if snapshots[1]["a"] != 30 {
		t.Errorf("snapshot isolation broken: snapshot[1][a]=%d, want 30", snapshots[1]["a"])
	}
}

// ---------------------------------------------------------------------------
// EndWith
// ---------------------------------------------------------------------------

func TestEndWith_Basic(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.EndWith(p, 4, 5))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d]: got %d, want %d", i, got[i], v)
		}
	}
}

func TestEndWith_EmptyUpstream(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{})
	got, err := kitsune.Collect(ctx, kitsune.EndWith(p, 10, 20))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 20}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d]: got %d, want %d", i, got[i], v)
		}
	}
}

func TestEndWith_NoItems(t *testing.T) {
	// EndWith with no suffix items is a no-op.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.EndWith(p))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEndWith_OrderGuarantee(t *testing.T) {
	// Suffix must appear strictly after all upstream items, not interleaved.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.EndWith(p, 99))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %v, want 6 items", got)
	}
	if got[5] != 99 {
		t.Errorf("last item: got %d, want 99", got[5])
	}
	for i := 0; i < 5; i++ {
		if got[i] != i+1 {
			t.Errorf("[%d]: got %d, want %d", i, got[i], i+1)
		}
	}
}
