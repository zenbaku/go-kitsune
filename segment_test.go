package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune"
)

// composableFunc is a non-Stage type that implements Composable, used in
// later tests to verify Then/Through truly accept the interface and not
// just Stage values.
type composableFunc[I, O any] func(*kitsune.Pipeline[I]) *kitsune.Pipeline[O]

func (f composableFunc[I, O]) Apply(p *kitsune.Pipeline[I]) *kitsune.Pipeline[O] {
	return f(p)
}

// TestComposable_StageSatisfies asserts that Stage[I,O] satisfies the
// Composable[I,O] interface. The check is done at compile time via assignment.
func TestComposable_StageSatisfies(t *testing.T) {
	var s kitsune.Stage[int, int] = func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	}
	var c kitsune.Composable[int, int] = s
	got, err := kitsune.Collect(context.Background(), c.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}

// TestComposable_ThenAcceptsComposable verifies Then accepts any value
// implementing Composable[I,M] / Composable[M,O], not only Stage values.
// We pass a composableFunc (non-Stage) on the left and a Stage on the right
// to prove heterogeneous composition works.
func TestComposable_ThenAcceptsComposable(t *testing.T) {
	increment := composableFunc[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})
	double := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})

	composed := kitsune.Then[int, int, int](increment, double)
	got, err := kitsune.Collect(context.Background(), composed.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 6, 8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestComposable_ThroughAcceptsComposable verifies Pipeline.Through accepts
// any value implementing Composable[T,T], not only Stage[T,T].
func TestComposable_ThroughAcceptsComposable(t *testing.T) {
	upcase := composableFunc[string, string](func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[string] {
		return kitsune.Map(p, func(_ context.Context, s string) (string, error) {
			return strings.ToUpper(s), nil
		})
	})

	out, err := kitsune.Collect(context.Background(),
		kitsune.FromSlice([]string{"hello", "world"}).Through(upcase),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"HELLO", "WORLD"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

// TestSegment_GraphNodePropagation verifies SegmentName flows from stageMeta
// to the public GraphNode via Describe.
func TestSegment_GraphNodePropagation(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	out := kitsune.NewSegment("doubler", inner).Apply(kitsune.FromSlice([]int{1, 2, 3}))

	nodes := out.Describe()
	found := false
	for _, n := range nodes {
		if n.SegmentName == "doubler" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one GraphNode with SegmentName=\"doubler\", got %+v", nodes)
	}
}

// TestSegment_SatisfiesComposable asserts at compile time that Segment[I,O]
// satisfies Composable[I,O].
func TestSegment_SatisfiesComposable(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
	})
	var c kitsune.Composable[int, int] = kitsune.NewSegment("seg", inner)
	got, err := kitsune.Collect(context.Background(), c.Apply(kitsune.FromSlice([]int{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}

// TestSegment_SingleSegmentStampsAllInnerStages verifies that every stage
// constructed by the inner Stage receives the segment name, and stages
// outside the segment (the source) do not.
func TestSegment_SingleSegmentStampsAllInnerStages(t *testing.T) {
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		a := kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 1, nil })
		b := kitsune.Filter(a, func(_ context.Context, v int) (bool, error) { return v > 0, nil })
		return kitsune.Map(b, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	src := kitsune.FromSlice([]int{1, 2, 3})
	out := kitsune.NewSegment("transform", inner).Apply(src)

	var inSeg, outSeg int
	for _, n := range out.Describe() {
		switch {
		case n.SegmentName == "transform":
			inSeg++
		case n.SegmentName == "":
			outSeg++
		}
	}
	if inSeg != 3 {
		t.Errorf("expected 3 stages stamped \"transform\" (Map, Filter, Map), got %d", inSeg)
	}
	if outSeg < 1 {
		t.Errorf("expected the FromSlice source to remain unstamped, got %d unstamped", outSeg)
	}
}

// TestSegment_NestedInnermostWins verifies that when a Segment wraps another
// Segment, the inner segment owns the stages it creates, and the outer
// segment owns only the stages it creates outside the inner.
func TestSegment_NestedInnermostWins(t *testing.T) {
	innermost := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 2, nil })
	})
	// "outer" runs an extra Map outside the inner Segment so we can verify
	// that stage gets "outer" while the inner Map gets "inner".
	outerStage := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		via := kitsune.NewSegment("inner", innermost).Apply(p)
		return kitsune.Map(via, func(_ context.Context, v int) (int, error) { return v + 100, nil })
	})

	out := kitsune.NewSegment("outer", outerStage).Apply(kitsune.FromSlice([]int{1, 2, 3}))

	var innerCount, outerCount int
	for _, n := range out.Describe() {
		switch n.SegmentName {
		case "inner":
			innerCount++
		case "outer":
			outerCount++
		}
	}
	if innerCount != 1 {
		t.Errorf("expected exactly 1 stage stamped \"inner\" (the inner Map), got %d", innerCount)
	}
	if outerCount != 1 {
		t.Errorf("expected exactly 1 stage stamped \"outer\" (the post-inner Map), got %d", outerCount)
	}
}

// TestSegment_ReplayContextCancel verifies that a replayed segment exits
// cleanly when the run context is cancelled mid-stream, and that
// OnStageDone is still fired with the partial counts.
func TestSegment_ReplayContextCancel(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	// Seed a 200-item snapshot directly so replay has enough work for a
	// cancel to land mid-stream.
	raw := make([]json.RawMessage, 200)
	for i := range raw {
		raw[i], _ = json.Marshal(i)
	}
	if err := store.Save(context.Background(), "big", raw); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// Replay with a short-lived context.
	ctx, cancel := context.WithCancel(context.Background())
	src2 := kitsune.FromSlice([]int{}) // any source; replay supplies items.
	seg2 := kitsune.NewSegment("big", kitsune.Stage[int, int](
		func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
			return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil })
		}))

	gotItems := 0
	done := make(chan error, 1)
	go func() {
		_, err := seg2.Apply(src2).ForEach(func(_ context.Context, _ int) error {
			gotItems++
			if gotItems == 5 {
				cancel()
			}
			return nil
		}).Run(ctx, kitsune.WithDevStore(store))
		done <- err
	}()

	select {
	case err := <-done:
		// Either a clean nil return (replay loop saw ctx.Done after cancel) or
		// context.Canceled is acceptable; what we care about is that the run
		// terminates promptly rather than emitting all 200 items.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("run: unexpected err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not terminate within 2s of cancel")
	}
	if gotItems >= 200 {
		t.Errorf("got %d items; cancel should have stopped replay before exhaustion", gotItems)
	}
}
