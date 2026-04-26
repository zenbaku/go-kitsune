package kitsune_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

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

	// Run 2: replay. Inspect the graph via a GraphHook and count
	// per-stage lifecycle events.
	src2 := kitsune.FromSlice([]int{1, 2, 3})
	seg2 := kitsune.NewSegment("enrich", enrich)

	gh := &recordingHook{}
	if _, err := collectWith(t, seg2.Apply(src2), kitsune.WithDevStore(store), kitsune.WithHook(gh)); err != nil {
		t.Fatalf("replay run: %v", err)
	}

	nodes := gh.nodes()
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
	if replay.ID == 0 {
		t.Errorf("replay node ID is zero; expected the segment-output id")
	}

	// The replay node must be wired into the graph: at least one
	// downstream node lists replay.ID in its Inputs.
	var downstream []int64
	for _, n := range nodes {
		for _, in := range n.Inputs {
			if in == replay.ID {
				downstream = append(downstream, n.ID)
			}
		}
	}
	if len(downstream) == 0 {
		t.Errorf("no downstream node references replay.ID=%d in Inputs; nodes=%+v", replay.ID, nodes)
	}

	// Lifecycle counters: replay must fire OnStageStart exactly once
	// for "enrich" and OnStageDone with processed=3, errors=0.
	if got := gh.startCount("enrich"); got != 1 {
		t.Errorf("OnStageStart(enrich) fired %d times, want 1", got)
	}
	processed, errs, ok := gh.donePayload("enrich")
	if !ok {
		t.Fatalf("OnStageDone(enrich) was not called")
	}
	if processed != 3 || errs != 0 {
		t.Errorf("OnStageDone(enrich) processed=%d errors=%d, want processed=3 errors=0", processed, errs)
	}
}

// recordingHook is a Hook + GraphHook that records the graph snapshot and
// counts lifecycle events per stage. It embeds [kithooks.NoopHook] so future
// additions to the [kithooks.Hook] interface do not silently no-op the test.
type recordingHook struct {
	kithooks.NoopHook

	mu       sync.Mutex
	graph    []kithooks.GraphNode
	starts   map[string]int
	dones    map[string]struct{ processed, errors int64 }
	doneSeen map[string]bool
}

func (h *recordingHook) OnGraph(nodes []kithooks.GraphNode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph = append([]kithooks.GraphNode(nil), nodes...)
}

func (h *recordingHook) OnStageStart(_ context.Context, stage string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.starts == nil {
		h.starts = make(map[string]int)
	}
	h.starts[stage]++
}

func (h *recordingHook) OnStageDone(_ context.Context, stage string, processed int64, errors int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dones == nil {
		h.dones = make(map[string]struct{ processed, errors int64 })
		h.doneSeen = make(map[string]bool)
	}
	h.dones[stage] = struct{ processed, errors int64 }{processed, errors}
	h.doneSeen[stage] = true
}

func (h *recordingHook) nodes() []kithooks.GraphNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.graph
}

func (h *recordingHook) startCount(stage string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts[stage]
}

func (h *recordingHook) donePayload(stage string) (processed, errors int64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.dones[stage]
	if !ok {
		return 0, 0, false
	}
	return d.processed, d.errors, h.doneSeen[stage]
}
