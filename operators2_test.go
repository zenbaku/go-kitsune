package kitsune_test

import (
	"context"
	"math/rand"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Reject
// ---------------------------------------------------------------------------

func TestReject(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.Reject(p, func(_ context.Context, v int) (bool, error) {
		return v%2 == 0, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// WithIndex
// ---------------------------------------------------------------------------

func TestWithIndex(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	got, err := kitsune.Collect(ctx, kitsune.WithIndex(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	for i, item := range got {
		if item.Index != i || item.Value != []string{"a", "b", "c"}[i] {
			t.Fatalf("item[%d] = %+v", i, item)
		}
	}
}

// ---------------------------------------------------------------------------
// Pairwise
// ---------------------------------------------------------------------------

func TestPairwise(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4})
	got, err := kitsune.Collect(ctx, kitsune.Pairwise(p))
	if err != nil {
		t.Fatal(err)
	}
	want := []kitsune.Pair[int, int]{{1, 2}, {2, 3}, {3, 4}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Fatalf("got[%d]=%v, want %v", i, got[i], p)
		}
	}
}

func TestPairwiseSingleItem(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{42})
	got, err := kitsune.Collect(ctx, kitsune.Pairwise(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// TakeEvery / DropEvery / MapEvery
// ---------------------------------------------------------------------------

func TestTakeEvery(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{0, 1, 2, 3, 4, 5, 6})
	got, err := kitsune.Collect(ctx, kitsune.TakeEvery(p, 3))
	if err != nil {
		t.Fatal(err)
	}
	// indices 0, 3, 6
	want := []int{0, 3, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDropEvery(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{0, 1, 2, 3, 4, 5})
	got, err := kitsune.Collect(ctx, kitsune.DropEvery(p, 2))
	if err != nil {
		t.Fatal(err)
	}
	// drop indices 0, 2, 4 → keep 1, 3, 5
	want := []int{1, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMapEvery(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	double := func(_ context.Context, v int) (int, error) { return v * 2, nil }
	got, err := kitsune.Collect(ctx, kitsune.MapEvery(p, 2, double))
	if err != nil {
		t.Fatal(err)
	}
	// indices 0, 2, 4 get doubled: 2, 2, 6, 4, 10
	want := []int{2, 2, 6, 4, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Intersperse
// ---------------------------------------------------------------------------

func TestIntersperse(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, kitsune.Intersperse(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 0, 2, 0, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestIntersperseEmpty(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{})
	got, err := kitsune.Collect(ctx, kitsune.Intersperse(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestIntersperseSingle(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{42})
	got, err := kitsune.Collect(ctx, kitsune.Intersperse(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ChunkBy
// ---------------------------------------------------------------------------

func TestChunkBy(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 1, 2, 2, 1})
	got, err := kitsune.Collect(ctx, kitsune.ChunkBy(p, func(v int) int { return v }))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 1}, {2, 2}, {1}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("chunk[%d]: got %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("chunk[%d][%d]: got %v, want %v", i, j, got[i][j], want[i][j])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// ChunkWhile
// ---------------------------------------------------------------------------

func TestChunkWhile(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3, 1, 2})
	// chunk while ascending
	got, err := kitsune.Collect(ctx, kitsune.ChunkWhile(p, func(prev, curr int) bool { return curr > prev }))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{1, 2, 3}, {1, 2}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("chunk[%d]: got %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("chunk[%d][%d]: got %v, want %v", i, j, got[i][j], want[i][j])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// MinMax / MinBy / MaxBy
// ---------------------------------------------------------------------------

func TestMinMax(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9, 2, 6})
	less := func(a, b int) bool { return a < b }
	pair, ok, err := kitsune.MinMax(ctx, p, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if pair.First != 1 || pair.Second != 9 {
		t.Fatalf("min=%d max=%d, want 1 9", pair.First, pair.Second)
	}
}

func TestMinMaxEmpty(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{})
	less := func(a, b int) bool { return a < b }
	_, ok, err := kitsune.MinMax(ctx, p, less)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for empty pipeline")
	}
}

func TestMinBy(t *testing.T) {
	ctx := context.Background()
	type item struct {
		name  string
		score int
	}
	p := kitsune.FromSlice([]item{{"a", 3}, {"b", 1}, {"c", 2}})
	less := func(a, b int) bool { return a < b }
	got, ok, err := kitsune.MinBy(ctx, p, func(v item) int { return v.score }, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.name != "b" {
		t.Fatalf("got %+v, want b", got)
	}
}

func TestMaxBy(t *testing.T) {
	ctx := context.Background()
	type item struct {
		name  string
		score int
	}
	p := kitsune.FromSlice([]item{{"a", 3}, {"b", 1}, {"c", 2}})
	less := func(a, b int) bool { return a < b }
	got, ok, err := kitsune.MaxBy(ctx, p, func(v item) int { return v.score }, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.name != "a" {
		t.Fatalf("got %+v, want a", got)
	}
}

// ---------------------------------------------------------------------------
// ReduceWhile
// ---------------------------------------------------------------------------

func TestReduceWhile(t *testing.T) {
	ctx := context.Background()
	// Sum until running total exceeds 10.
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6})
	sum, err := kitsune.ReduceWhile(ctx, p, 0, func(acc, v int) (int, bool) {
		next := acc + v
		return next, next <= 10
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1+2+3+4=10 (continue), 10+5=15 (stop) → returns 15
	if sum != 15 {
		t.Fatalf("got %d, want 15", sum)
	}
}

func TestReduceWhileNeverStops(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	sum, err := kitsune.ReduceWhile(ctx, p, 0, func(acc, v int) (int, bool) {
		return acc + v, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum != 6 {
		t.Fatalf("got %d, want 6", sum)
	}
}

// ---------------------------------------------------------------------------
// TakeRandom
// ---------------------------------------------------------------------------

func TestTakeRandom(t *testing.T) {
	ctx := context.Background()
	rand.Seed(42)
	items := makeInts(100)
	p := kitsune.FromSlice(items)
	got, err := kitsune.TakeRandom(ctx, p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d items, want 10", len(got))
	}
	// All returned items must be from the original set.
	set := make(map[int]bool)
	for _, v := range items {
		set[v] = true
	}
	for _, v := range got {
		if !set[v] {
			t.Fatalf("returned value %d not in source", v)
		}
	}
}

func TestTakeRandomFewerThanN(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.TakeRandom(ctx, p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	sort.Ints(got)
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Stage / Then / Through
// ---------------------------------------------------------------------------

func TestStage(t *testing.T) {
	ctx := context.Background()
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, double(p))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 2 || got[1] != 4 || got[2] != 6 {
		t.Fatalf("got %v", got)
	}
}

func TestThen(t *testing.T) {
	ctx := context.Background()
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	stringify := kitsune.Stage[int, string](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, v int) (string, error) { return string(rune('0' + v)), nil })
	})
	combined := kitsune.Then(double, stringify)

	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, combined(p))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "2" || got[1] != "4" || got[2] != "6" {
		t.Fatalf("got %v", got)
	}
}

func TestStage_IsolatedTesting(t *testing.T) {
	ctx := context.Background()

	parse := kitsune.Stage[string, int](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, kitsune.Lift(strconv.Atoi))
	})
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, n int) (int, error) { return n * 2, nil })
	})

	parsed, err := parse.Apply(kitsune.FromSlice([]string{"1", "2", "3"})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 || parsed[0] != 1 || parsed[1] != 2 || parsed[2] != 3 {
		t.Fatalf("parse stage: expected [1 2 3], got %v", parsed)
	}

	doubled, err := double.Apply(kitsune.FromSlice(parsed)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(doubled) != 3 || doubled[0] != 2 || doubled[1] != 4 || doubled[2] != 6 {
		t.Fatalf("double stage: expected [2 4 6], got %v", doubled)
	}
}

func TestThrough(t *testing.T) {
	ctx := context.Background()
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	addOne := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})

	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Collect(ctx, p.Through(double).Through(addOne))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 3 || got[1] != 5 || got[2] != 7 {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Sort / SortBy / Unzip
// ---------------------------------------------------------------------------

func TestSort(t *testing.T) {
	ctx := context.Background()
	results, err := kitsune.Sort(
		kitsune.FromSlice([]int{3, 1, 4, 1, 5, 9, 2, 6}),
		func(a, b int) bool { return a < b },
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 1, 2, 3, 4, 5, 6, 9}
	if len(results) != len(want) {
		t.Fatalf("got %v, want %v", results, want)
	}
	for i, v := range results {
		if v != want[i] {
			t.Fatalf("results[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestSortBy(t *testing.T) {
	ctx := context.Background()
	type item struct{ Name string }
	results, err := kitsune.SortBy(
		kitsune.FromSlice([]item{{"banana"}, {"apple"}, {"cherry"}}),
		func(x item) string { return x.Name },
		func(a, b string) bool { return a < b },
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Name != "apple" || results[1].Name != "banana" || results[2].Name != "cherry" {
		t.Errorf("unexpected order: %v", results)
	}
}

func TestUnzip(t *testing.T) {
	ctx := context.Background()
	pairs := []kitsune.Pair[int, string]{
		{First: 1, Second: "a"},
		{First: 2, Second: "b"},
		{First: 3, Second: "c"},
	}
	src := kitsune.FromSlice(pairs)
	as, bs := kitsune.Unzip(src)

	var aResults []int
	var bResults []string

	r1 := as.ForEach(func(_ context.Context, v int) error {
		aResults = append(aResults, v)
		return nil
	}).Build()
	r2 := bs.ForEach(func(_ context.Context, v string) error {
		bResults = append(bResults, v)
		return nil
	}).Build()

	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}

	sort.Ints(aResults)
	sort.Strings(bResults)
	if len(aResults) != 3 || aResults[0] != 1 || aResults[1] != 2 || aResults[2] != 3 {
		t.Errorf("aResults: got %v, want [1 2 3]", aResults)
	}
	if len(bResults) != 3 || bResults[0] != "a" || bResults[1] != "b" || bResults[2] != "c" {
		t.Errorf("bResults: got %v, want [a b c]", bResults)
	}
}

// ---------------------------------------------------------------------------
// Pairwise edge cases (6i)
// ---------------------------------------------------------------------------

func TestPairwiseEmpty(t *testing.T) {
	ctx := context.Background()
	got, err := kitsune.Pairwise(kitsune.FromSlice([]int{})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 pairs, got %d: %v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// ConcatMap edge cases (6i)
// ---------------------------------------------------------------------------

func TestConcatMapIgnoresConcurrency(t *testing.T) {
	// ConcatMap must enforce serial execution even when Concurrency(N) is passed.
	// Verify by checking that output order is fully preserved.
	p := kitsune.FromSlice([]int{3, 1, 2})
	got := collectAll(t, kitsune.ConcatMap(p,
		func(_ context.Context, v int, yield func(int) error) error {
			for i := 0; i < v; i++ {
				if err := yield(v); err != nil {
					return err
				}
			}
			return nil
		},
		kitsune.Concurrency(5), // must be overridden to 1 by ConcatMap
	))
	want := []int{3, 3, 3, 1, 2, 2}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v (order must be preserved with serial execution)", got, want)
	}
}

// ---------------------------------------------------------------------------
// SwitchMap edge cases (6i)
// ---------------------------------------------------------------------------

func TestSwitchMapCancelsInner(t *testing.T) {
	// When a new upstream item arrives, the current inner pipeline must be cancelled.
	ctx := context.Background()
	ch := kitsune.NewChannel[int](10)

	innerStarted := make(chan struct{})
	var cancelled atomic.Bool

	done := make(chan []int, 1)
	go func() {
		got := collectAll(t, kitsune.SwitchMap(ch.Source(), func(innerCtx context.Context, v int, yield func(int) error) error {
			if v == 1 {
				close(innerStarted)
				select {
				case <-innerCtx.Done():
					cancelled.Store(true)
					return innerCtx.Err()
				case <-time.After(5 * time.Second):
					return yield(v) // should not reach here
				}
			}
			return yield(v * 10)
		}))
		done <- got
	}()

	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	<-innerStarted // wait for inner for item 1 to start

	if err := ch.Send(ctx, 2); err != nil { // should cancel inner for item 1
		t.Fatal(err)
	}
	ch.Close()

	got := <-done
	if !cancelled.Load() {
		t.Error("inner pipeline for item 1 was not cancelled by item 2")
	}
	if len(got) == 0 {
		t.Fatal("expected output from SwitchMap")
	}
}

func TestSwitchMapContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := kitsune.NewChannel[int](10)

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.SwitchMap(ch.Source(), func(innerCtx context.Context, _ int, yield func(int) error) error {
			select {
			case <-innerCtx.Done():
				return innerCtx.Err()
			case <-time.After(5 * time.Second):
				return yield(42)
			}
		}).Collect(ctx)
		done <- err
	}()

	time.Sleep(5 * time.Millisecond)
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
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

// ---------------------------------------------------------------------------
// ExhaustMap edge cases (6i)
// ---------------------------------------------------------------------------

func TestExhaustMapIgnoresDuringActive(t *testing.T) {
	// Items arriving while inner is running must be dropped.
	ctx := context.Background()
	ch := kitsune.NewChannel[int](10)

	innerRunning := make(chan struct{})
	releaseInner := make(chan struct{})

	var processed atomic.Int64
	results := make(chan []int, 1)
	go func() {
		got := collectAll(t, kitsune.ExhaustMap(ch.Source(), func(_ context.Context, v int, yield func(int) error) error {
			processed.Add(1)
			if v == 1 {
				close(innerRunning)
				<-releaseInner
			}
			return yield(v)
		}))
		results <- got
	}()

	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	<-innerRunning // wait until inner for item 1 is running

	// These arrive while inner is active — should be dropped
	_ = ch.Send(ctx, 2)
	_ = ch.Send(ctx, 3)
	time.Sleep(10 * time.Millisecond) // let ExhaustMap read and discard 2 and 3

	close(releaseInner) // release inner for item 1
	ch.Close()

	got := <-results
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("expected [1] (items 2,3 dropped while active), got %v", got)
	}
	if processed.Load() != 1 {
		t.Errorf("expected 1 processed item, got %d", processed.Load())
	}
}

func TestExhaustMapContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := kitsune.NewChannel[int](10)

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.ExhaustMap(ch.Source(), func(innerCtx context.Context, _ int, yield func(int) error) error {
			select {
			case <-innerCtx.Done():
				return innerCtx.Err()
			case <-time.After(5 * time.Second):
				return yield(42)
			}
		}).Collect(ctx)
		done <- err
	}()

	time.Sleep(5 * time.Millisecond)
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
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
