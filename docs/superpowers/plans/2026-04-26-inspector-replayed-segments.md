# Inspector visibility for replayed Segments — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make replayed Segments first-class observable units. When `WithDevStore` causes a segment to replay from snapshot, the replay goroutine becomes a proper synthetic stage in `rc.stages`/`rc.metas`, fires `OnStageStart` / `OnItem` / `OnStageDone`, appears in the `OnGraph` payload with `Kind="segment-replay"`, and renders on the inspector dashboard with a REPLAY badge inside the segment hull.

**Architecture:** Rewrite `tryReplaySegment` (currently an out-of-band `go func()`) to allocate the output channel, build a `stageFunc` that runs the replay loop while firing hook events and respecting `ctx.Done()`, construct a `stageMeta` (kind=`segment-replay`, segmentName=name, id reused from the segment's outermost output Pipeline so downstream wiring is unbroken), and register both via `rc.add`. The build wrapper in `Segment.Apply` passes `output.id` into `tryReplaySegment` so the synthetic stage's id matches the id downstream stages already reference. `captureSegment` is unchanged — inner stages already fire normally during capture.

**Tech Stack:** Go generics, the existing `kitsune.Hook` interface, the inspector SVG/HTML dashboard.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Background notes

### Why id reuse is safe

`Segment.Apply` captures `before := globalIDSeq` then runs `s.stage.Apply(p)`, which constructs the inner stage chain and assigns IDs in `(before, after]`. The pipeline returned by `s.stage.Apply(p)` (`output`) has `id == after` (or some id within that range; in any case unique to this run). When replay is taken, `originalBuild(rc)` is *not* called, so none of the inner-stage IDs in that range get registered in `rc.metas`. Reusing `output.id` for the synthetic stage's `meta.id` means the id is unique within `rc.metas` *and* matches the id that downstream stages already reference in their `inputs`. No fresh `nextPipelineID()` call is required.

### Hook safety

`rc.hook` is set in `Runner.Run` before any stage goroutine starts. The synthetic stage's `stageFunc` reads `rc.hook` once at goroutine start (matching how operator stages already do it) and falls back to `internal.NoopHook{}` if it is nil. All hook calls are made on the same goroutine, so no extra synchronisation is required.

### What does *not* change

- `captureSegment` — capture mode already fires inner-stage hook events via `originalBuild(rc)`.
- `InspectorStore` JSON shape — synthetic stages are persisted as ordinary entries in the existing `Stages` map.
- The `Hook` interface or any optional extension interface — pure reuse.
- The `GraphNode` struct — `Kind` is already a `string`, just gains a new well-known value `"segment-replay"`.

---

## File structure

| File | Responsibility | Change |
|---|---|---|
| `segment.go` | Segment build wrapper + replay/capture helpers | Rewrite `tryReplaySegment`; tweak `Segment.Apply` to pass id |
| `segment_devstore_test.go` | DevStore round-trip and graph-shape tests | Add 2 tests (graph-shape, decode-failure) |
| `segment_test.go` | Segment composition + lifecycle tests | Add 1 test (context cancel during replay) |
| `inspector/inspector_test.go` | Inspector wiring tests | Add 2 tests (/state shape, capture-then-replay roundtrip) |
| `inspector/ui.html` | Dashboard | Add REPLAY badge in node renderer + dash in latency cell |
| `doc/inspector.md` | Inspector dashboard guide | Document the REPLAY badge and `segment-replay` stage kind |
| `doc/dev-iteration.md` | DevStore guide | Cross-link inspector behavior on replay |
| `doc/roadmap.md` | Project roadmap | Mark item `[x]` and re-write description in past tense |
| Memory: `project_higher_level_authoring.md` | User auto-memory | Note replayed segments are now first-class observable stages |

---

## Tasks

### Task 1: Rewrite `tryReplaySegment` to register a synthetic stage

The core change. After this task, replayed segments appear as a single `kind="segment-replay"` node in `Pipeline.Describe()` and emit `OnStageStart`/`OnItem`/`OnStageDone` events. Decode failures and context cancellation are surfaced.

**Files:**
- Modify: `segment.go`

- [ ] **Step 1: Write the failing graph-shape test**

Append to `segment_devstore_test.go`:

```go
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
```

Add a helper at the bottom of the file (or top, near `collectWith`) for the GraphHook:

```go
// graphHookFunc adapts a function to satisfy hooks.Hook + hooks.GraphHook.
// It implements all Hook methods as no-ops; only OnGraph is meaningful.
type graphHookFunc func(nodes []kithooks.GraphNode)

func (f graphHookFunc) OnStageStart(_ context.Context, _ string)                          {}
func (f graphHookFunc) OnItem(_ context.Context, _ string, _ time.Duration, _ error)      {}
func (f graphHookFunc) OnStageDone(_ context.Context, _ string, _ int64, _ int64)         {}
func (f graphHookFunc) OnGraph(nodes []kithooks.GraphNode)                                { f(nodes) }
```

If imports `kithooks "github.com/zenbaku/go-kitsune/hooks"` and `time` are not already present in the test file header, add them. The existing imports at the top of `segment_devstore_test.go` are: `"context"`, `"encoding/json"`, `"reflect"`, `"sync/atomic"`, `"testing"`, and `"github.com/zenbaku/go-kitsune"`. After this step they should also include `"time"` and `kithooks "github.com/zenbaku/go-kitsune/hooks"`.

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test -run TestSegmentDevStore_ReplayAppearsInGraph ./... -count=1`
Expected: FAIL with `no segment-replay node in graph; got: [...]`. The current `tryReplaySegment` is out-of-band so no node appears.

- [ ] **Step 3: Update `Segment.Apply` to pass `output.id` into `tryReplaySegment`**

In `segment.go`, find the build wrapper assignment (around line 91):

```go
output.build = func(rc *runCtx) chan O {
    for _, id := range ids {
        rc.segmentByID[id] = name
    }

    if rc.devStore != nil && name != "" {
        if replayed, ok := tryReplaySegment[O](rc, name); ok {
            return replayed
        }
        return captureSegment[O](rc, name, originalBuild(rc))
    }
    return originalBuild(rc)
}
```

Replace the `tryReplaySegment` call site:

```go
        if replayed, ok := tryReplaySegment[O](rc, name, output.id); ok {
            return replayed
        }
```

- [ ] **Step 4: Rewrite `tryReplaySegment` to register a synthetic stage**

Replace the existing function body in `segment.go` (lines ~116-147) with:

```go
// tryReplaySegment attempts to load a snapshot for name from rc.devStore.
// On success it registers a synthetic stage in rc.stages/rc.metas and
// returns the new stage's output channel and true. The synthetic stage's
// goroutine fires OnStageStart, then per-item OnItem (with zero duration),
// then OnStageDone, so the run is observable via [Hook] and renders as
// a node with Kind="segment-replay" in [GraphHook] payloads.
//
// id is the segment-output Pipeline's id; reusing it keeps downstream
// stage inputs wired correctly (downstream's track(p) recorded inputs
// against this id during construction).
//
// On miss (any error from store.Load) the function returns nil, false;
// the caller falls back to capture mode (run live).
func tryReplaySegment[T any](rc *runCtx, name string, id int64) (chan T, bool) {
    raws, err := rc.devStore.Load(context.Background(), name)
    if err != nil {
        return nil, false
    }

    out := make(chan T, internal.DefaultBuffer)

    fn := func(ctx context.Context) error {
        hook := rc.hook
        if hook == nil {
            hook = internal.NoopHook{}
        }
        hook.OnStageStart(ctx, name)

        var processed, errs int64
        defer close(out)
        defer func() { hook.OnStageDone(ctx, name, processed, errs) }()

        for _, data := range raws {
            select {
            case <-ctx.Done():
                return ctx.Err()
            default:
            }

            var item T
            if err := json.Unmarshal(data, &item); err != nil {
                errs++
                hook.OnItem(ctx, name, 0, err)
                return nil
            }

            select {
            case <-ctx.Done():
                return ctx.Err()
            case out <- item:
                processed++
                hook.OnItem(ctx, name, 0, nil)
            }
        }
        return nil
    }

    meta := stageMeta{
        id:          id,
        name:        name,
        kind:        "segment-replay",
        segmentName: name,
        getChanLen:  func() int { return len(out) },
        getChanCap:  func() int { return cap(out) },
    }
    rc.add(fn, meta)

    return out, true
}
```

Add `"github.com/zenbaku/go-kitsune/internal"` to the import block if not already present. Verify imports include: `"context"`, `"encoding/json"`, `"sync/atomic"`, `"github.com/zenbaku/go-kitsune/internal"`.

- [ ] **Step 5: Build to verify**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 6: Run the new test and verify it passes**

Run: `go test -run TestSegmentDevStore_ReplayAppearsInGraph ./... -count=1`
Expected: PASS.

- [ ] **Step 7: Run existing DevStore tests to verify no regressions**

Run: `go test -run TestSegmentDevStore ./... -count=1 -v`
Expected: all PASS, including `TestSegmentDevStore_CaptureThenReplay` and `TestSegmentDevStore_NoStore`.

- [ ] **Step 8: Commit**

```bash
git add segment.go segment_devstore_test.go
git commit -m "$(cat <<'EOF'
feat(segment): register replayed segments as synthetic stages

tryReplaySegment now registers a kind="segment-replay" stage in
rc.stages/rc.metas instead of running an out-of-band goroutine, so
replayed segments are visible to GraphHook and emit OnStageStart /
OnItem / OnStageDone like any other stage. Decode failures surface
via OnItem and context cancellation exits cleanly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Test context cancellation during replay

Verifies the new `select`-per-item path exits cleanly when the run context is cancelled mid-replay.

**Files:**
- Modify: `segment_test.go`

- [ ] **Step 1: Write the failing test**

Append to `segment_test.go`:

```go
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
```

Imports needed: `"errors"`, `"encoding/json"`, `"time"`. Add any not already in the file's import block.

- [ ] **Step 2: Run the test and verify it passes**

Run: `go test -run TestSegment_ReplayContextCancel ./... -count=1`
Expected: PASS. (The implementation from Task 1 already includes the `select`-per-item paths needed; this test exists to lock in that behaviour and prevent regression.)

- [ ] **Step 3: Run with the race detector**

Run: `go test -race -run TestSegment_ReplayContextCancel ./... -count=1`
Expected: PASS, no races.

- [ ] **Step 4: Commit**

```bash
git add segment_test.go
git commit -m "$(cat <<'EOF'
test(segment): verify replay exits cleanly on context cancel

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Test decode-failure visibility on replay

Verifies that when a snapshot contains an item that fails `json.Unmarshal`, the replay stage records exactly one error via `OnItem`, stops emission, and downstream sees only the items emitted before the failure.

**Files:**
- Modify: `segment_devstore_test.go`

- [ ] **Step 1: Write the failing test**

Append to `segment_devstore_test.go`:

```go
// TestSegmentDevStore_ReplayDecodeFailure verifies that a corrupt item in
// a snapshot is reported via OnItem(err) and that emission stops at the
// failing item rather than silently aborting.
func TestSegmentDevStore_ReplayDecodeFailure(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	// Hand-craft a snapshot: one valid int, one corrupt blob, one trailing valid int.
	// Only the first item should reach downstream.
	raw := []json.RawMessage{
		json.RawMessage(`42`),
		json.RawMessage(`"not-an-int"`),
		json.RawMessage(`99`),
	}
	if err := store.Save(context.Background(), "decode", raw); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// Replay through a typed Segment[int,int].
	innerCalls := 0
	seg := kitsune.NewSegment("decode", kitsune.Stage[int, int](
		func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
			return kitsune.Map(p, func(_ context.Context, v int) (int, error) {
				innerCalls++
				return v, nil
			})
		}))

	// Counting hook to verify the OnItem(err) call.
	mh := kitsune.NewMetricsHook()

	out, err := collectWith(t, seg.Apply(kitsune.FromSlice([]int{})),
		kitsune.WithDevStore(store), kitsune.WithHook(mh))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !reflect.DeepEqual(out, []int{42}) {
		t.Errorf("got %v, want [42] (replay should stop at decode failure)", out)
	}
	if innerCalls != 0 {
		t.Errorf("innerCalls=%d, want 0 (replay should bypass inner stage)", innerCalls)
	}

	snap := mh.Snapshot()
	stageStats, ok := snap.Stages["decode"]
	if !ok {
		t.Fatalf("metrics: no Stages[\"decode\"] entry; got: %+v", snap.Stages)
	}
	if stageStats.Errors != 1 {
		t.Errorf("metrics Errors=%d, want 1", stageStats.Errors)
	}
	if stageStats.Processed != 1 {
		t.Errorf("metrics Processed=%d, want 1", stageStats.Processed)
	}
}
```

- [ ] **Step 2: Run the test and verify it passes**

Run: `go test -run TestSegmentDevStore_ReplayDecodeFailure ./... -count=1`
Expected: PASS. (The implementation in Task 1 already surfaces decode failures.)

- [ ] **Step 3: Commit**

```bash
git add segment_devstore_test.go
git commit -m "$(cat <<'EOF'
test(segment): verify decode failures during replay surface via OnItem

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Inspector tests for synthetic replay stage

Verifies the inspector renders a replayed segment as a stage in `/state` with the correct `Status`, `Items`, and graph metadata. Also verifies the capture-then-replay roundtrip end-to-end through the inspector.

**Files:**
- Modify: `inspector/inspector_test.go`

- [ ] **Step 1: Write the failing happy-path test**

Append to `inspector/inspector_test.go`:

```go
// TestInspector_SegmentReplay_Visible verifies that when a Segment replays
// from a DevStore snapshot, it appears in /state as a stage with kind
// "segment-replay", and the run's graph payload includes the matching node.
func TestInspector_SegmentReplay_Visible(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	// Seed a snapshot directly.
	raws := []json.RawMessage{
		json.RawMessage(`1`),
		json.RawMessage(`2`),
		json.RawMessage(`3`),
	}
	if err := store.Save(context.Background(), "ingest", raws); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	insp := inspector.New()
	defer insp.Close()

	src := kitsune.FromSlice([]int{})
	seg := kitsune.NewSegment("ingest", kitsune.Stage[int, int](
		func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
			return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil })
		}))

	got := []int{}
	_, err := seg.Apply(src).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("downstream got %v, want [1 2 3]", got)
	}

	// Allow ticker (250ms) to flush a stats broadcast and /state to read.
	time.Sleep(300 * time.Millisecond)

	state := getState(t, insp.URL())

	var node *kithooks.GraphNode
	for i := range state.Graph {
		if state.Graph[i].Kind == "segment-replay" {
			node = &state.Graph[i]
			break
		}
	}
	if node == nil {
		t.Fatalf("no segment-replay node in /state graph; got: %+v", state.Graph)
	}
	if node.Name != "ingest" || node.SegmentName != "ingest" {
		t.Errorf("graph node Name=%q SegmentName=%q, want both %q",
			node.Name, node.SegmentName, "ingest")
	}

	raw, ok := state.Stats["ingest"]
	if !ok {
		t.Fatalf("no Stats[\"ingest\"] in /state; got keys: %v", keys(state.Stats))
	}
	var snap struct {
		Items  int64  `json:"items"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode stage snapshot: %v", err)
	}
	if snap.Items != 3 {
		t.Errorf("Items=%d, want 3", snap.Items)
	}
	if snap.Status != "done" {
		t.Errorf("Status=%q, want \"done\"", snap.Status)
	}
}

// keys is a small helper for diagnostic output in test failure messages.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// reflect must be imported in this file already; add it if not.
```

If `"reflect"` is not in the imports, add it. The existing test file imports `"context"`, `"encoding/json"`, `"errors"`, `"io"`, `"net/http"`, `"testing"`, `"time"`, `kitsune`, `kithooks`, `inspector` — `reflect` may need adding.

- [ ] **Step 2: Run and verify it passes**

Run: `go test -run TestInspector_SegmentReplay_Visible ./inspector/ -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Write the capture-then-replay roundtrip test**

Append below the previous test in `inspector/inspector_test.go`:

```go
// TestInspector_SegmentReplay_CaptureThenReplay verifies an end-to-end
// roundtrip: run 1 captures (inner stages fire normal events), run 2
// replays (synthetic segment-replay stage replaces the inner-stage events).
func TestInspector_SegmentReplay_CaptureThenReplay(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	stage := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 100, nil },
			kitsune.WithName("inner-map"))
	})

	// --- Run 1: capture ---
	insp1 := inspector.New()
	src1 := kitsune.FromSlice([]int{1, 2})
	seg1 := kitsune.NewSegment("roundtrip", stage)
	out1 := []int{}
	if _, err := seg1.Apply(src1).ForEach(func(_ context.Context, v int) error {
		out1 = append(out1, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp1)); err != nil {
		t.Fatalf("capture run: %v", err)
	}
	if !reflect.DeepEqual(out1, []int{101, 102}) {
		t.Fatalf("capture out=%v, want [101 102]", out1)
	}

	time.Sleep(300 * time.Millisecond)
	state1 := getState(t, insp1.URL())
	insp1.Close()

	// Capture run: graph should contain the inner Map (stage name "inner-map"),
	// no segment-replay node.
	var sawInner1, sawReplay1 bool
	for _, n := range state1.Graph {
		if n.Name == "inner-map" {
			sawInner1 = true
		}
		if n.Kind == "segment-replay" {
			sawReplay1 = true
		}
	}
	if !sawInner1 {
		t.Errorf("capture run: graph missing inner-map node; got: %+v", state1.Graph)
	}
	if sawReplay1 {
		t.Errorf("capture run: graph unexpectedly has segment-replay node")
	}

	// --- Run 2: replay ---
	insp2 := inspector.New()
	defer insp2.Close()
	src2 := kitsune.FromSlice([]int{})
	seg2 := kitsune.NewSegment("roundtrip", stage)
	out2 := []int{}
	if _, err := seg2.Apply(src2).ForEach(func(_ context.Context, v int) error {
		out2 = append(out2, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp2)); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if !reflect.DeepEqual(out2, []int{101, 102}) {
		t.Fatalf("replay out=%v, want [101 102]", out2)
	}

	time.Sleep(300 * time.Millisecond)
	state2 := getState(t, insp2.URL())

	// Replay run: graph should contain segment-replay node with name
	// "roundtrip"; no inner-map node.
	var sawInner2, sawReplay2 bool
	for _, n := range state2.Graph {
		if n.Name == "inner-map" {
			sawInner2 = true
		}
		if n.Kind == "segment-replay" && n.Name == "roundtrip" {
			sawReplay2 = true
		}
	}
	if sawInner2 {
		t.Errorf("replay run: graph unexpectedly has inner-map node")
	}
	if !sawReplay2 {
		t.Errorf("replay run: graph missing segment-replay node; got: %+v", state2.Graph)
	}
}
```

- [ ] **Step 4: Run and verify both inspector tests pass**

Run: `go test -run "TestInspector_SegmentReplay" ./inspector/ -count=1 -v`
Expected: 2 PASS.

- [ ] **Step 5: Run the full inspector suite with race**

Run: `go test -race ./inspector/ -count=1`
Expected: PASS, no races.

- [ ] **Step 6: Commit**

```bash
git add inspector/inspector_test.go
git commit -m "$(cat <<'EOF'
test(inspector): verify segment-replay nodes surface in /state

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: UI rendering — REPLAY badge and dashed avg-latency

Add a third corner-badge case and a kind-aware avg-latency cell to `inspector/ui.html`.

**Files:**
- Modify: `inspector/ui.html`

- [ ] **Step 1: Add a CSS class for the REPLAY badge**

In the `<style>` block in `inspector/ui.html`, find the existing effect-badge CSS (search for `eff-badge-required` around line 169). Append a sibling rule:

```css
.eff-badge-replay      { fill: var(--blue); fill-opacity: .85; }
```

The reuse of the `eff-badge-glyph` class for the inner text (already defined nearby) means no new text style is needed.

- [ ] **Step 2: Add the REPLAY badge in the SVG node renderer**

Find the effect-badge block in the node renderer (search for `if (n.is_effect)` around line 601). Below the closing `}` of the `if (n.is_effect)` block and before `root.appendChild(g);`, add:

```javascript
    // Segment-replay badge: top-right corner, blue REPLAY pill.
    if (n.kind === 'segment-replay') {
      const bw = 36, bh = 12;
      const bx = x + NW - bw - 4, by = y + 4;
      const bg = document.createElementNS(NS, 'rect');
      bg.setAttribute('x', bx); bg.setAttribute('y', by);
      bg.setAttribute('width', bw); bg.setAttribute('height', bh);
      bg.setAttribute('rx', 3); bg.setAttribute('ry', 3);
      bg.setAttribute('class', 'eff-badge-replay');
      const bt = document.createElementNS(NS, 'text');
      bt.setAttribute('x', bx + bw/2); bt.setAttribute('y', by + bh - 3);
      bt.setAttribute('text-anchor', 'middle');
      bt.setAttribute('class', 'eff-badge-glyph');
      bt.textContent = 'REPLAY';
      g.appendChild(bg); g.appendChild(bt);
    }
```

- [ ] **Step 3: Render avg-latency as `—` for segment-replay rows in the table**

In `renderTable()` (search for `<td>${fmtNs(s.avgLatNs)}</td>` around line 919), replace that cell with a kind-aware version. The full row template (the `tr.innerHTML = …` for the `else` branch) currently reads:

```javascript
      tr.innerHTML = `
        <td style="font-weight:600">${esc(n.name)}</td>
        <td class="v-dim" style="font-size:9px;letter-spacing:.06em;text-transform:uppercase">${esc(n.kind)}</td>
        <td class="s-${s.status}">${sdot(s.status)} ${s.status}</td>
        <td>${s.items>0 ? fmtBig(s.items) : '<span class="v-dim">-</span>'}</td>
        <td class="${s.errors   >0?'v-err' :'v-dim'}">${s.errors   >0?fmtBig(s.errors)  :'-'}</td>
        <td class="${s.drops    >0?'v-warn':'v-dim'}">${s.drops    >0?fmtBig(s.drops)   :'-'}</td>
        <td class="${s.restarts >0?'v-warn':'v-dim'}">${s.restarts >0?fmtBig(s.restarts):'-'}</td>
        <td>${mkSparkline(sparkData.get(n.name)||[], s.ratePerSec)}</td>
        <td>${fmtNs(s.avgLatNs)}</td>
        <td>${mkBufBar(s.bufferLen, s.bufferCap)}</td>`;
```

Change the `<td>${fmtNs(s.avgLatNs)}</td>` line to:

```javascript
        <td>${n.kind === 'segment-replay' ? '<span class="v-dim">—</span>' : fmtNs(s.avgLatNs)}</td>
```

- [ ] **Step 4: Render avg-latency as `—` in the sidebar metrics grid**

In `openSidebar()` (search for `'Avg Latency'` around line 841), the row currently reads:

```javascript
    ['Avg Latency',  s ? fmtNs(s.avgLatNs) : '-'],
```

Change to:

```javascript
    ['Avg Latency',  s ? (n.kind === 'segment-replay' ? '<span class="v-dim">—</span>' : fmtNs(s.avgLatNs)) : '-'],
```

- [ ] **Step 5: Manual visual smoke test**

Build the Effect example, but the simpler check is to write a tiny throwaway program that runs a Segment with WithDevStore twice (once to seed, once to replay) with `inspector.New()` attached, then open the URL and visually confirm:

1. After the first run, the segment hull contains the inner stages and no REPLAY badge.
2. After the second run (with the inspector reset by re-running the program), the segment hull contains a single node with the REPLAY badge in the top-right and `—` in the Avg Latency column.

This step is optional (the unit tests cover correctness); skip if visual setup is inconvenient.

- [ ] **Step 6: Commit**

```bash
git add inspector/ui.html
git commit -m "$(cat <<'EOF'
feat(inspector): render REPLAY badge for segment-replay nodes

Adds a blue corner badge for nodes with kind="segment-replay" and
renders avg-latency as an em dash in both the stage table and sidebar
since replayed items have no meaningful per-item duration.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Documentation

Document the new badge and the segment-replay stage kind in the inspector guide; cross-link from the dev-iteration guide.

**Files:**
- Modify: `doc/inspector.md`
- Modify: `doc/dev-iteration.md`

- [ ] **Step 1: Add a "Replayed segments" subsection to `doc/inspector.md`**

Find the "Pipeline Graph" section (or the subsection that describes node kinds and badges). Append after the existing badge documentation:

```markdown
### Replayed segments

When the run is started with [`WithDevStore`](dev-iteration.md) and a snapshot exists for a named [`Segment`](operators.md#segment), the segment's inner stages are bypassed and the cached items are streamed directly to the segment's output. The dashboard renders this as a single node with kind `segment-replay`, a blue **REPLAY** badge in the top-right corner, and an em dash in the Avg Latency column (replayed items carry no per-item duration). The segment hull and label remain in place around the synthetic node.

This makes it visually obvious whether a given run executed the segment live or replayed it from cache. Capture-mode runs are unchanged: the inner stages render normally inside the hull.
```

- [ ] **Step 2: Cross-link from `doc/dev-iteration.md`**

Find the section describing replay behavior (search for "replay" in the dev-iteration guide). Append to that section:

```markdown
When the [Inspector](inspector.md) is attached to a run that replays a snapshot, the replayed segment renders as a single node with the **REPLAY** badge in the segment hull. This makes capture and replay visually distinguishable on the dashboard.
```

- [ ] **Step 3: Verify docs build cleanly**

The repo uses plain Markdown, no static-site generator; the verification is just to read the diff and confirm no broken inline links.

Run: `git diff doc/`
Expected: only the additions described above; existing prose untouched.

- [ ] **Step 4: Commit**

```bash
git add doc/inspector.md doc/dev-iteration.md
git commit -m "$(cat <<'EOF'
docs: describe REPLAY badge and segment-replay stage kind

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Roadmap and memory

Mark the roadmap item complete and update the user's auto-memory note.

**Files:**
- Modify: `doc/roadmap.md`
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`

- [ ] **Step 1: Mark the roadmap item `[x]`**

In `doc/roadmap.md`, find the line that begins:

```markdown
- [ ] **Inspector visibility for replayed Segments**: When `WithDevStore` is attached and a snapshot exists, `tryReplaySegment` spawns an out-of-band goroutine
```

Replace the entire bullet with:

```markdown
- [x] **Inspector visibility for replayed Segments** *(shipped 2026-04-26)*: `tryReplaySegment` now registers the replay loop as a synthetic stage in `rc.stages`/`rc.metas` with `Kind="segment-replay"`, `Name=<segment-name>`, and `SegmentName=<segment-name>`. The replay goroutine fires `OnStageStart` / `OnItem` (zero duration; non-nil err on per-item decode failure) / `OnStageDone`, so `MetricsHook`, user `Hook` implementations, and the inspector dashboard see replayed segments as first-class observable units. The dashboard renders a blue **REPLAY** badge in the segment hull and shows avg-latency as an em dash. Plan at `docs/superpowers/plans/2026-04-26-inspector-replayed-segments.md`.
```

- [ ] **Step 2: Update the memory note**

Read the current contents of `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`. Append a sentence to the section that describes the inspector / RunSummary / Segment work:

```markdown
Replayed segments (`WithDevStore` snapshot present) now register as a synthetic stage with `Kind="segment-replay"` so they fire normal `Hook` events and render with a REPLAY badge in the inspector hull (2026-04-26).
```

If the file does not have a clearly relevant section, append the sentence as a new line at the end.

- [ ] **Step 3: Commit roadmap change**

```bash
git add doc/roadmap.md
git commit -m "$(cat <<'EOF'
docs(roadmap): mark inspector replayed-segments item complete

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

The memory file is outside the repo and is not committed.

---

### Task 8: Final verification

- [ ] **Step 1: Full test suite**

Run: `task test`
Expected: PASS.

- [ ] **Step 2: Race test suite**

Run: `task test:race`
Expected: PASS, no races. The new replay path under hook calls is the riskiest concurrency change.

- [ ] **Step 3: Property tests**

Run: `task test:property`
Expected: PASS. (Property tests are not directly affected by this change but should be re-run as a sanity check.)

- [ ] **Step 4: Examples**

Run: `task test:examples`
Expected: PASS.

- [ ] **Step 5: Inform the user**

Concise report: number of commits made, gates passed, one-line summary of what is now visible on the dashboard for replayed segments.

---

## Self-review notes (for the plan author, not the engineer)

- **Spec coverage:** every section of the design spec is mapped to a task. Architecture → Task 1; hook contract → Task 1; GraphNode wire format → Task 1 (no struct change, just `Kind` value); UI rendering → Task 5; edge cases (empty / decode-fail / ctx-cancel / no-DevStore) → Tasks 1, 2, 3; testing → Tasks 1-4; documentation → Task 6; roadmap → Task 7; final verification → Task 8.
- **Type-name consistency:** `tryReplaySegment` signature gains an `id int64` parameter; `Segment.Apply` passes `output.id`; `stageMeta.kind` value is `"segment-replay"` everywhere; the synthetic stage's name field is the segment name (no suffix).
- **No placeholders.** Every step shows the actual content needed.
- **Risk:** Task 1 step 4 inserts code inside a generic function with type parameter `T`. The `getChanLen` / `getChanCap` closures capture `out` (a `chan T`) and call `len`/`cap`, both of which work on typed channels in Go. Verified by analogy: existing operator stages (e.g. `Map`) capture typed output channels in identical closures.
- **Sequencing.** Tasks 1-4 are sequential (each depends on the previous compiling and passing). Task 5 (UI) is independent of Tasks 2-4 but must come after Task 1. Tasks 6 and 7 are tail items. Task 8 is the final gate.
