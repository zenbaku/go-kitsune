package kitsune_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune"
	kithooks "github.com/zenbaku/go-kitsune/hooks"
)

// collectWith runs the pipeline as a ForEach terminal that appends items to
// a slice, with the supplied RunOptions. It is the test-only equivalent of
// kitsune.Collect when RunOptions need to be passed.
func collectWith[T any](t *testing.T, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) ([]T, error) {
	t.Helper()
	var out []T
	_, err := p.ForEach(func(_ context.Context, item T) error {
		out = append(out, item)
		return nil
	}).Run(context.Background(), opts...)
	return out, err
}

// TestSegmentDevStore_CaptureThenReplay verifies the round-trip: first run
// captures, second run replays without invoking the inner stage.
func TestSegmentDevStore_CaptureThenReplay(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	var liveCalls atomic.Int32
	enrich := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			liveCalls.Add(1)
			return v * 10, nil
		})
	})

	runOnce := func() []int {
		src := kitsune.FromSlice([]int{1, 2, 3})
		seg := kitsune.NewSegment("enrich", enrich)
		out, err := collectWith(t, seg.Apply(src), kitsune.WithDevStore(store))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return out
	}

	first := runOnce()
	if !reflect.DeepEqual(first, []int{10, 20, 30}) {
		t.Fatalf("first run: got %v, want [10 20 30]", first)
	}
	if liveCalls.Load() != 3 {
		t.Errorf("first run: liveCalls=%d, want 3", liveCalls.Load())
	}

	liveCalls.Store(0)
	second := runOnce()
	if !reflect.DeepEqual(second, []int{10, 20, 30}) {
		t.Fatalf("second run: got %v, want [10 20 30] from snapshot", second)
	}
	if liveCalls.Load() != 0 {
		t.Errorf("second run: liveCalls=%d, want 0 (replay should bypass inner stage)", liveCalls.Load())
	}
}

// TestSegmentDevStore_NoStore verifies that without WithDevStore, segments
// behave as before (no capture, no replay).
func TestSegmentDevStore_NoStore(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	seg := kitsune.NewSegment("noop", kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil })
	}))
	got, err := kitsune.Collect(ctx, seg.Apply(src))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("got %v, want [1 2 3]", got)
	}
}

// TestSegmentDevStore_EmptyNameIgnored verifies that segments with empty
// names are not captured or replayed.
func TestSegmentDevStore_EmptyNameIgnored(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	ctx := context.Background()

	src := kitsune.FromSlice([]int{1, 2, 3})
	p := kitsune.Map(src, func(_ context.Context, v int) (int, error) { return v + 1, nil })

	runner := p.ForEach(func(_ context.Context, _ int) error { return nil })
	if _, err := runner.Run(ctx, kitsune.WithDevStore(store)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, ""); err == nil {
		t.Errorf("expected ErrSnapshotMissing for empty-name segment")
	}
}

// TestSegmentDevStore_ReplayBypassesInnerEffects verifies that replay does
// not invoke side effects in the inner stage.
func TestSegmentDevStore_ReplayBypassesInnerEffects(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	var sideEffects atomic.Int32
	inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			sideEffects.Add(1)
			return v, nil
		})
	})
	seg := kitsune.NewSegment("inner-effects", inner)

	// First run: live, side effects fire.
	if _, err := collectWith(t, seg.Apply(kitsune.FromSlice([]int{1, 2})), kitsune.WithDevStore(store)); err != nil {
		t.Fatal(err)
	}
	firstRunCalls := sideEffects.Load()
	if firstRunCalls != 2 {
		t.Fatalf("first run side-effects = %d, want 2", firstRunCalls)
	}

	// Second run: replay; side effects do NOT fire.
	if _, err := collectWith(t, seg.Apply(kitsune.FromSlice([]int{1, 2})), kitsune.WithDevStore(store)); err != nil {
		t.Fatal(err)
	}
	if sideEffects.Load() != firstRunCalls {
		t.Errorf("second run side-effects = %d, want unchanged at %d", sideEffects.Load(), firstRunCalls)
	}
}

// TestSegmentDevStore_FormatPreservesItems verifies that the on-disk JSON
// format is a flat array of marshaled items, suitable for FromCheckpoint.
func TestSegmentDevStore_FormatPreservesItems(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	ctx := context.Background()

	type Pair struct{ K, V int }
	seg := kitsune.NewSegment("pairs", kitsune.Stage[Pair, Pair](func(p *kitsune.Pipeline[Pair]) *kitsune.Pipeline[Pair] {
		return kitsune.Map(p, func(_ context.Context, x Pair) (Pair, error) { return x, nil })
	}))
	src := kitsune.FromSlice([]Pair{{1, 10}, {2, 20}})
	if _, err := collectWith(t, seg.Apply(src), kitsune.WithDevStore(store)); err != nil {
		t.Fatal(err)
	}

	raw, err := store.Load(ctx, "pairs")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("len(raw)=%d, want 2", len(raw))
	}
	var p0 Pair
	if err := json.Unmarshal(raw[0], &p0); err != nil {
		t.Fatal(err)
	}
	if p0 != (Pair{1, 10}) {
		t.Errorf("raw[0]=%+v, want {1 10}", p0)
	}
}

// TestSegmentDevStore_ReplayAppearsInGraph verifies that a replayed segment
// is registered as a single synthetic stage with kind="segment-replay" in
// the run's graph (visible via Pipeline.Describe), so the inspector and any
// GraphHook can render it as a real node.
func TestSegmentDevStore_ReplayAppearsInGraph(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	enrich := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v * 10, nil })
	})

	// Run 1: capture.
	src1 := kitsune.FromSlice([]int{1, 2, 3})
	seg1 := kitsune.NewSegment("enrich", enrich)
	if _, err := collectWith(t, seg1.Apply(src1), kitsune.WithDevStore(store)); err != nil {
		t.Fatalf("capture run: %v", err)
	}

	// Run 2: replay. Inspect the graph via a GraphHook.
	src2 := kitsune.FromSlice([]int{1, 2, 3})
	seg2 := kitsune.NewSegment("enrich", enrich)

	var nodes []kithooks.GraphNode
	gh := graphHookFunc(func(n []kithooks.GraphNode) { nodes = n })

	if _, err := collectWith(t, seg2.Apply(src2), kitsune.WithDevStore(store), kitsune.WithHook(gh)); err != nil {
		t.Fatalf("replay run: %v", err)
	}

	var replay *kithooks.GraphNode
	for i := range nodes {
		if nodes[i].Kind == "segment-replay" {
			replay = &nodes[i]
			break
		}
	}
	if replay == nil {
		t.Fatalf("no segment-replay node in graph; got: %+v", nodes)
	}
	if replay.Name != "enrich" {
		t.Errorf("replay node Name=%q, want %q", replay.Name, "enrich")
	}
	if replay.SegmentName != "enrich" {
		t.Errorf("replay node SegmentName=%q, want %q", replay.SegmentName, "enrich")
	}
}

// graphHookFunc adapts a function to satisfy hooks.Hook + hooks.GraphHook.
// It implements all Hook methods as no-ops; only OnGraph is meaningful.
type graphHookFunc func(nodes []kithooks.GraphNode)

func (f graphHookFunc) OnStageStart(_ context.Context, _ string)                     {}
func (f graphHookFunc) OnItem(_ context.Context, _ string, _ time.Duration, _ error) {}
func (f graphHookFunc) OnStageDone(_ context.Context, _ string, _ int64, _ int64)    {}
func (f graphHookFunc) OnGraph(nodes []kithooks.GraphNode)                           { f(nodes) }
