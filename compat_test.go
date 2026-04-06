package kitsune_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// ConsecutiveDedup
// ---------------------------------------------------------------------------

func TestConsecutiveDedup(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 1, 2, 2, 2, 3, 1, 1})
	got, err := kitsune.Collect(ctx, kitsune.ConsecutiveDedup(p))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestConsecutiveDedupEmpty(t *testing.T) {
	ctx := context.Background()
	got, err := kitsune.Collect(ctx, kitsune.ConsecutiveDedup(kitsune.FromSlice([]int{})))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ConsecutiveDedupBy
// ---------------------------------------------------------------------------

func TestConsecutiveDedupBy(t *testing.T) {
	ctx := context.Background()
	type event struct {
		kind  string
		value int
	}
	items := []event{
		{"a", 1}, {"a", 2}, {"b", 3}, {"b", 4}, {"a", 5},
	}
	p := kitsune.FromSlice(items)
	got, err := kitsune.Collect(ctx, kitsune.ConsecutiveDedupBy(p, func(e event) string { return e.kind }))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %v", got)
	}
	if got[0].kind != "a" || got[1].kind != "b" || got[2].kind != "a" {
		t.Fatalf("unexpected order: %v", got)
	}
}

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
// DeadLetter
// ---------------------------------------------------------------------------

func TestDeadLetter(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("even numbers fail")

	ok, dlq := kitsune.DeadLetter(kitsune.FromSlice([]int{1, 2, 3, 4, 5}),
		func(_ context.Context, v int) (string, error) {
			if v%2 == 0 {
				return "", boom
			}
			return "ok", nil
		},
	)

	var okItems []string
	var dlqItems []kitsune.ErrItem[int]
	r1 := ok.ForEach(func(_ context.Context, s string) error { okItems = append(okItems, s); return nil }).Build()
	r2 := dlq.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error { dlqItems = append(dlqItems, e); return nil }).Build()
	merged, _ := kitsune.MergeRunners(r1, r2)
	if err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if len(okItems) != 3 {
		t.Fatalf("expected 3 ok items, got %d: %v", len(okItems), okItems)
	}
	if len(dlqItems) != 2 {
		t.Fatalf("expected 2 dlq items, got %d: %v", len(dlqItems), dlqItems)
	}
	for _, e := range dlqItems {
		if e.Item%2 != 0 {
			t.Errorf("expected even item in dlq, got %d", e.Item)
		}
	}
}

// ---------------------------------------------------------------------------
// DeadLetterSink
// ---------------------------------------------------------------------------

func TestDeadLetterSink(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("write failed")

	var written []int
	dlq, runner := kitsune.DeadLetterSink(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) error {
			if v == 2 {
				return boom
			}
			written = append(written, v)
			return nil
		},
	)

	var dlqItems []kitsune.ErrItem[int]
	dlqRunner := dlq.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error {
		dlqItems = append(dlqItems, e)
		return nil
	}).Build()
	merged, _ := kitsune.MergeRunners(runner, dlqRunner)

	if err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if len(written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(written), written)
	}
	if len(dlqItems) != 1 || dlqItems[0].Item != 2 || !errors.Is(dlqItems[0].Err, boom) {
		t.Fatalf("unexpected dlq: %v", dlqItems)
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

// ---------------------------------------------------------------------------
// WindowByTime
// ---------------------------------------------------------------------------

func TestWindowByTime(t *testing.T) {
	ctx := context.Background()

	// Produce 6 items spread across ~3 windows of 20ms each.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 6; i++ {
			if !yield(i) {
				return nil
			}
			time.Sleep(8 * time.Millisecond)
		}
		return nil
	})

	windows, err := kitsune.Collect(ctx, kitsune.WindowByTime(p, 20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	// We expect at least 2 windows and all 6 items accounted for.
	total := 0
	for _, w := range windows {
		total += len(w)
	}
	if total != 6 {
		t.Fatalf("expected 6 total items across windows, got %d in %d windows", total, len(windows))
	}
	if len(windows) < 2 {
		t.Fatalf("expected at least 2 windows, got %d", len(windows))
	}
}

func TestWindowByTimePartialFlush(t *testing.T) {
	ctx := context.Background()
	// Source completes before window fires — partial window must be flushed.
	p := kitsune.FromSlice([]int{1, 2, 3})
	windows, err := kitsune.Collect(ctx, kitsune.WindowByTime(p, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 || len(windows[0]) != 3 {
		t.Fatalf("expected 1 window with 3 items, got %v", windows)
	}
}
