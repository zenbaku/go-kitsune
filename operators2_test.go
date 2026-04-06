package kitsune_test

import (
	"context"
	"math/rand"
	"sort"
	"testing"

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
