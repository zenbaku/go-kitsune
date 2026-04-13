package kitsune_test

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// ---------------------------------------------------------------------------
// Catch
// ---------------------------------------------------------------------------

func TestCatch_NoError(t *testing.T) {
	// Primary succeeds — fallback must never be invoked.
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	fallbackCalled := false
	out := kitsune.Catch(p, func(err error) *kitsune.Pipeline[int] {
		fallbackCalled = true
		return kitsune.FromSlice([]int{99})
	})
	got, err := kitsune.Collect(ctx, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
	if fallbackCalled {
		t.Fatal("fallback should not be called when primary succeeds")
	}
}

func TestCatch_ErrorTriggersFallback(t *testing.T) {
	// Primary errors after emitting two items; fallback provides the rest.
	ctx := context.Background()
	boom := errors.New("boom")
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 3 {
				return 0, boom
			}
			return v * 10, nil
		},
	)
	var caught error
	out := kitsune.Catch(p, func(err error) *kitsune.Pipeline[int] {
		caught = err
		return kitsune.FromSlice([]int{99, 100})
	})
	got, err := kitsune.Collect(ctx, out)
	if err != nil {
		t.Fatalf("unexpected error from Catch: %v", err)
	}
	if !sliceEqual(got, []int{10, 20, 99, 100}) {
		t.Fatalf("got %v, want [10 20 99 100]", got)
	}
	if !errors.Is(caught, boom) {
		t.Fatalf("fallback received error %v, want %v", caught, boom)
	}
}

func TestCatch_FallbackEmpty(t *testing.T) {
	// Primary errors; fallback emits nothing.
	ctx := context.Background()
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) {
			return 0, errors.New("oops")
		},
	)
	out := kitsune.Catch(p, func(error) *kitsune.Pipeline[int] {
		return kitsune.FromSlice([]int{})
	})
	got, err := kitsune.Collect(ctx, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestCatch_ContextCancelledSkipsFallback(t *testing.T) {
	// If the context is already cancelled, the fallback must not be invoked.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fallbackCalled := false
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		return errors.New("source error")
	})
	out := kitsune.Catch(p, func(error) *kitsune.Pipeline[int] {
		fallbackCalled = true
		return kitsune.FromSlice([]int{1})
	})
	_, _ = kitsune.Collect(ctx, out)
	if fallbackCalled {
		t.Fatal("fallback must not be called when context is cancelled")
	}
}

func TestCatch_FallbackAlsoErrors(t *testing.T) {
	// Primary errors; fallback also errors — fallback's error is returned.
	ctx := context.Background()
	fallbackErr := errors.New("fallback failure")
	p := kitsune.Map(kitsune.FromSlice([]int{1}),
		func(_ context.Context, v int) (int, error) { return 0, errors.New("primary") },
	)
	out := kitsune.Catch(p, func(error) *kitsune.Pipeline[int] {
		return kitsune.Map(kitsune.FromSlice([]int{1}),
			func(_ context.Context, v int) (int, error) { return 0, fallbackErr },
		)
	})
	_, err := kitsune.Collect(ctx, out)
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("expected fallback error %v, got %v", fallbackErr, err)
	}
}

// ---------------------------------------------------------------------------
// Using
// ---------------------------------------------------------------------------

func TestUsing_ItemsFlow(t *testing.T) {
	// Resource wraps a slice; pipeline emits its items.
	p := kitsune.Using(
		func(_ context.Context) ([]int, error) { return []int{1, 2, 3}, nil },
		func(s []int) *kitsune.Pipeline[int] { return kitsune.FromSlice(s) },
		func(_ []int) {},
	)
	got := collectAll(t, p)
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
}

func TestUsing_ReleaseCalledOnSuccess(t *testing.T) {
	var released bool
	p := kitsune.Using(
		func(_ context.Context) (int, error) { return 42, nil },
		func(r int) *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{r}) },
		func(_ int) { released = true },
	)
	collectAll(t, p)
	if !released {
		t.Fatal("release not called on success")
	}
}

func TestUsing_ReleaseCalledOnError(t *testing.T) {
	boom := errors.New("boom")
	var released bool
	p := kitsune.Using(
		func(_ context.Context) (int, error) { return 0, nil },
		func(_ int) *kitsune.Pipeline[int] {
			return kitsune.Map(kitsune.FromSlice([]int{1}),
				func(_ context.Context, _ int) (int, error) { return 0, boom },
			)
		},
		func(_ int) { released = true },
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if !released {
		t.Fatal("release not called on error")
	}
}

func TestUsing_ReleaseCalledOnCancel(t *testing.T) {
	var released bool
	ctx, cancel := context.WithCancel(context.Background())
	ch := kitsune.NewChannel[int](0)
	p := kitsune.Using(
		func(_ context.Context) (int, error) { return 0, nil },
		func(_ int) *kitsune.Pipeline[int] { return ch.Source() },
		func(_ int) { released = true },
	)
	done := make(chan error, 1)
	go func() {
		done <- p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	}()
	cancel()
	<-done
	if !released {
		t.Fatal("release not called on cancellation")
	}
}

func TestUsing_AcquireError(t *testing.T) {
	// If acquire fails, the pipeline errors and release is NOT called.
	boom := errors.New("acquire failed")
	var released bool
	p := kitsune.Using(
		func(_ context.Context) (int, error) { return 0, boom },
		func(_ int) *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1}) },
		func(_ int) { released = true },
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.ForEach(func(_ context.Context, _ int) error { return nil }).Run(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("expected acquire error, got %v", err)
	}
	if released {
		t.Fatal("release must not be called when acquire fails")
	}
}

func TestUsing_ReleaseCalledOnEarlyStop(t *testing.T) {
	var released bool
	p := kitsune.Using(
		func(_ context.Context) ([]int, error) { return []int{1, 2, 3, 4, 5}, nil },
		func(s []int) *kitsune.Pipeline[int] { return kitsune.FromSlice(s) },
		func(_ []int) { released = true },
	)
	got := collectAll(t, kitsune.Take(p, 2))
	if !sliceEqual(got, []int{1, 2}) {
		t.Fatalf("got %v, want [1 2]", got)
	}
	if !released {
		t.Fatal("release not called on early stop")
	}
}

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
	want := []kitsune.Consecutive[int]{{Prev: 1, Curr: 2}, {Prev: 2, Curr: 3}, {Prev: 3, Curr: 4}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d]=%v, want %v", i, got[i], w)
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
	result, ok, err := kitsune.MinMax(ctx, p, less)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if result.Min != 1 || result.Max != 9 {
		t.Fatalf("min=%d max=%d, want 1 9", result.Min, result.Max)
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

// ---------------------------------------------------------------------------
// SwitchMap — Timeout
// ---------------------------------------------------------------------------

func TestSwitchMapTimeout(t *testing.T) {
	// Inner fn sleeps longer than the deadline; the item context should be
	// cancelled, causing the fn to return an error and the pipeline to halt.
	p := kitsune.FromSlice([]int{1})
	_, err := kitsune.SwitchMap(p, func(ctx context.Context, v int, yield func(int) error) error {
		select {
		case <-time.After(5 * time.Second):
			return yield(v)
		case <-ctx.Done():
			return ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestSwitchMapTimeoutSkip(t *testing.T) {
	// Slow item with OnError(Skip) — the timed-out item is dropped, no pipeline error.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.SwitchMap(p, func(ctx context.Context, v int, yield func(int) error) error {
		if v == 2 {
			select {
			case <-time.After(5 * time.Second):
				return yield(v)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return yield(v * 10)
	},
		kitsune.Timeout(20*time.Millisecond),
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background())

	if err != nil {
		t.Fatalf("expected nil error with Skip, got: %v", err)
	}
	// Items 1 and 3 processed; item 2 timed out and dropped.
	if len(got) == 0 {
		t.Fatal("expected some output, got none")
	}
	for _, v := range got {
		if v == 2 {
			t.Errorf("timed-out item 2 should not appear in output, got %v", got)
		}
	}
}

func TestSwitchMapTimeoutFastFn(t *testing.T) {
	// Fast fn completes well within deadline — no error, all items processed.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.SwitchMap(p, func(_ context.Context, v int, yield func(int) error) error {
		return yield(v * 10)
	}, kitsune.Timeout(time.Second)).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected output, got none")
	}
}

// ---------------------------------------------------------------------------
// SwitchMap — Supervise
// ---------------------------------------------------------------------------

// waitForRestarts polls hook until at least n restarts are recorded or the
// deadline passes. It returns true if the count was reached in time.
func waitForRestarts(hook *testkit.RecordingHook, n int, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if len(hook.Restarts()) >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestSwitchMapSupervise(t *testing.T) {
	// Send items one at a time via an unbuffered channel so that each item is
	// only in-flight when we explicitly put it there. Item 1 errors, the stage
	// restarts (detected via hook), then item 2 is processed successfully.
	ctx := context.Background()
	hook := &testkit.RecordingHook{}
	ch := kitsune.NewChannel[int](0)

	errored := make(chan struct{})
	var calls atomic.Int64

	resultCh := make(chan struct {
		items []int
		err   error
	}, 1)
	go func() {
		items, err := kitsune.SwitchMap(ch.Source(), func(_ context.Context, v int, yield func(int) error) error {
			c := calls.Add(1)
			if c == 1 {
				close(errored)
				return errors.New("transient switchmap error")
			}
			return yield(v * 10)
		},
			kitsune.Supervise(kitsune.RestartOnError(3, nil)),
			kitsune.WithName("sm-sup"),
		).Collect(ctx, kitsune.WithHook(hook))
		resultCh <- struct {
			items []int
			err   error
		}{items, err}
	}()

	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Wait for fn to signal it errored, then wait for hook to record the restart.
	select {
	case <-errored:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for item 1 to error")
	}
	if !waitForRestarts(hook, 1, 2*time.Second) {
		t.Fatal("timeout waiting for stage restart")
	}

	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	ch.Close()

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("unexpected error after supervised restart: %v", r.err)
		}
		if len(r.items) == 0 {
			t.Fatal("expected output after restart, got none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pipeline to complete")
	}
}

func TestSwitchMapSuperviseExhausted(t *testing.T) {
	// Send one item per restart cycle so that each restart sees a fresh item.
	// After MaxRestarts the pipeline must return an error.
	ctx := context.Background()
	hook := &testkit.RecordingHook{}
	ch := kitsune.NewChannel[int](0)

	errored := make(chan struct{}, 10) // buffered so fn never blocks on send
	resultCh := make(chan error, 1)
	go func() {
		_, err := kitsune.SwitchMap(ch.Source(), func(_ context.Context, _ int, _ func(int) error) error {
			errored <- struct{}{}
			return errors.New("always fails")
		},
			kitsune.Supervise(kitsune.RestartOnError(2, nil)),
			kitsune.WithName("sm-sup-ex"),
		).Collect(ctx, kitsune.WithHook(hook))
		resultCh <- err
	}()

	// RestartOnError(2): allows 2 restarts (3 total attempts). Send 3 items,
	// one per attempt, each time waiting for the previous error to propagate.
	for i := 0; i < 3; i++ {
		if err := ch.Send(ctx, i+1); err != nil {
			t.Fatalf("Send(%d): %v", i+1, err)
		}
		select {
		case <-errored:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for fn to error on attempt %d", i+1)
		}
		if i < 2 {
			// Wait for the restart to be recorded before sending the next item.
			if !waitForRestarts(hook, i+1, 2*time.Second) {
				t.Fatalf("timeout waiting for restart %d", i+1)
			}
		}
	}

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("expected error after restarts exhausted, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pipeline to fail")
	}
}

// ---------------------------------------------------------------------------
// SwitchMap — Overflow
// ---------------------------------------------------------------------------

func TestSwitchMapOverflow(t *testing.T) {
	// A slow consumer with DropNewest overflow must not deadlock.
	// The pipeline should complete without hanging.
	p := kitsune.FromSlice([]int{1, 2, 3})
	done := make(chan struct{})
	go func() {
		defer close(done)
		p2 := kitsune.SwitchMap(p, func(_ context.Context, v int, yield func(int) error) error {
			for i := 0; i < 10; i++ {
				if err := yield(v*100 + i); err != nil {
					return err
				}
			}
			return nil
		},
			kitsune.Buffer(1),
			kitsune.Overflow(kitsune.DropNewest),
		)
		// Slow consumer: simulate backpressure.
		ctx := context.Background()
		seq, errFn := kitsune.Iter(ctx, p2)
		for range seq {
			time.Sleep(time.Millisecond)
		}
		if err := errFn(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline deadlocked under DropNewest overflow")
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap — Timeout
// ---------------------------------------------------------------------------

func TestExhaustMapTimeout(t *testing.T) {
	// Inner fn sleeps longer than the deadline; the item context should be
	// cancelled, causing the fn to return an error and the pipeline to halt.
	p := kitsune.FromSlice([]int{1})
	_, err := kitsune.ExhaustMap(p, func(ctx context.Context, v int, yield func(int) error) error {
		select {
		case <-time.After(5 * time.Second):
			return yield(v)
		case <-ctx.Done():
			return ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestExhaustMapTimeoutSkip(t *testing.T) {
	// Slow item with OnError(Skip) — the timed-out item is dropped, no pipeline error.
	// Use a slow source so item 1 is fully processed before item 2 arrives.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for _, v := range []int{1, 2, 3} {
			if !yield(v) {
				return nil
			}
			time.Sleep(150 * time.Millisecond) // space items out beyond the 20ms timeout
		}
		return nil
	})
	got, err := kitsune.ExhaustMap(p, func(ctx context.Context, v int, yield func(string) error) error {
		if v == 2 {
			select {
			case <-time.After(5 * time.Second):
				return yield("slow")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return yield("ok")
	},
		kitsune.Timeout(20*time.Millisecond),
		kitsune.OnError(kitsune.Skip()),
	).Collect(context.Background())

	if err != nil {
		t.Fatalf("expected nil error with Skip, got: %v", err)
	}
	for _, v := range got {
		if v == "slow" {
			t.Errorf("timed-out item should not appear in output, got %v", got)
		}
	}
}

func TestExhaustMapTimeoutFastFn(t *testing.T) {
	// Fast fn completes well within deadline — no error, all items processed.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.ExhaustMap(p, func(_ context.Context, v int, yield func(int) error) error {
		return yield(v * 10)
	}, kitsune.Timeout(time.Second)).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected output, got none")
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap — Supervise
// ---------------------------------------------------------------------------

func TestExhaustMapSupervise(t *testing.T) {
	// Same controlled-delivery pattern as the SwitchMap variant.
	ctx := context.Background()
	hook := &testkit.RecordingHook{}
	ch := kitsune.NewChannel[int](0)

	errored := make(chan struct{})
	var calls atomic.Int64

	resultCh := make(chan struct {
		items []int
		err   error
	}, 1)
	go func() {
		items, err := kitsune.ExhaustMap(ch.Source(), func(_ context.Context, v int, yield func(int) error) error {
			c := calls.Add(1)
			if c == 1 {
				close(errored)
				return errors.New("transient exhaustmap error")
			}
			return yield(v * 10)
		},
			kitsune.Supervise(kitsune.RestartOnError(3, nil)),
			kitsune.WithName("em-sup"),
		).Collect(ctx, kitsune.WithHook(hook))
		resultCh <- struct {
			items []int
			err   error
		}{items, err}
	}()

	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errored:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for item 1 to error")
	}
	if !waitForRestarts(hook, 1, 2*time.Second) {
		t.Fatal("timeout waiting for stage restart")
	}

	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	ch.Close()

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("unexpected error after supervised restart: %v", r.err)
		}
		if len(r.items) == 0 {
			t.Fatal("expected output after restart, got none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pipeline to complete")
	}
}

func TestExhaustMapSuperviseExhausted(t *testing.T) {
	ctx := context.Background()
	hook := &testkit.RecordingHook{}
	ch := kitsune.NewChannel[int](0)

	errored := make(chan struct{}, 10)
	resultCh := make(chan error, 1)
	go func() {
		_, err := kitsune.ExhaustMap(ch.Source(), func(_ context.Context, _ int, _ func(string) error) error {
			errored <- struct{}{}
			return errors.New("always fails")
		},
			kitsune.Supervise(kitsune.RestartOnError(2, nil)),
			kitsune.WithName("em-sup-ex"),
		).Collect(ctx, kitsune.WithHook(hook))
		resultCh <- err
	}()

	for i := 0; i < 3; i++ {
		if err := ch.Send(ctx, i+1); err != nil {
			t.Fatalf("Send(%d): %v", i+1, err)
		}
		select {
		case <-errored:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for fn to error on attempt %d", i+1)
		}
		if i < 2 {
			if !waitForRestarts(hook, i+1, 2*time.Second) {
				t.Fatalf("timeout waiting for restart %d", i+1)
			}
		}
	}

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("expected error after restarts exhausted, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for pipeline to fail")
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap — Overflow
// ---------------------------------------------------------------------------

func TestExhaustMapOverflow(t *testing.T) {
	// A slow consumer with DropNewest overflow must not deadlock.
	p := kitsune.FromSlice([]int{1, 2, 3})
	done := make(chan struct{})
	go func() {
		defer close(done)
		p2 := kitsune.ExhaustMap(p, func(_ context.Context, v int, yield func(int) error) error {
			for i := 0; i < 10; i++ {
				if err := yield(v*100 + i); err != nil {
					return err
				}
			}
			return nil
		},
			kitsune.Buffer(1),
			kitsune.Overflow(kitsune.DropNewest),
		)
		ctx := context.Background()
		seq, errFn := kitsune.Iter(ctx, p2)
		for range seq {
			time.Sleep(time.Millisecond)
		}
		if err := errFn(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline deadlocked under DropNewest overflow")
	}
}
