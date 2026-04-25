# Higher-Level Authoring Layer — M4 (DevStore + FromCheckpoint) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-segment snapshot/replay storage for development iteration. `WithDevStore(store)` RunOption captures each `Segment`'s output on first run and replays from snapshot on subsequent runs; `FromCheckpoint[T](store, name)` is a test-time source that loads a stored snapshot directly. Strictly dev-only: no schema versioning, no production safety.

**Architecture:** A `DevStore` interface defines `Save(ctx, segment, []json.RawMessage)` and `Load(ctx, segment)`. The built-in `FileDevStore` writes one JSON file per segment under a configurable directory. `WithDevStore(store)` populates `runConfig.devStore`, threaded into `runCtx.devStore`. `Segment.Apply` wraps its output's `build` closure: when `rc.devStore` is set, it tries `Load(name)` first; on hit, it skips the inner stage and emits the snapshot; on miss, it runs the inner stage live and tees output into a capture buffer that `Save`s at segment exit. `FromCheckpoint[T]` is an independent source that loads from a `DevStore` at run time.

**Tech Stack:** Go generics, `encoding/json`, the existing `Segment` machinery from M1.

**Module path:** `github.com/zenbaku/go-kitsune`

---

## Spec source

This plan implements Section 4 of `docs/superpowers/specs/2026-04-15-higher-level-authoring-design.md` (approved 2026-04-15). M1 (Segment), M2 (Effect), and M3 (RunSummary) are shipped. M4 is the final milestone of the higher-level authoring layer.

### Spec deltas applied during M4 design

These pragmatic scoping decisions override the spec text where they conflict.

1. **Drop `ReplayThrough(segmentName)` from v1.** The spec says "replay all segments up to and including `segmentName`, then run subsequent segments live" — but "up to and including" and "subsequent" are ambiguous in branched DAGs (segments can run on parallel forks). The pragmatic use case "replay everything except segment X so I can iterate on X" is already covered by `WithDevStore` plus manual snapshot deletion: delete X's snapshot file, run with `WithDevStore`, X runs live, others replay from snapshot. Defer formal `ReplayThrough` to a follow-up if a concrete user request demands it.

2. **`FromCheckpoint[T]` unmarshals via encoding/json directly**, not the run's Codec. The Codec lives in `runCtx` (run time); `FromCheckpoint` is a source constructed at pipeline-build time. Since `FileDevStore` writes JSON, hardcoding `json.Unmarshal` is consistent with the file format. If a future user wants a non-JSON `DevStore` impl, the `DevStore` interface is type-erased (`[]json.RawMessage`) so encoding mismatch is the user's problem.

3. **Capture is best-effort.** If `store.Save` fails after a successful run, log the error and return the original run result. The pipeline ran live; the dev-only affordance just didn't persist. This matches the dev-only framing: a failed save shouldn't escalate to a run failure.

4. **Capture is buffered in memory, not streaming.** Items accumulate in an in-memory `[]json.RawMessage` slice during the run; `Save` is called once at segment exit. The spec's `Save([]json.RawMessage)` signature already requires this. For very large segments this is a memory cost the user accepts when opting into DevStore.

5. **Capture/replay is keyed by segment NAME, not segment ID.** Two segments with the same name share a snapshot. This is intentional: in dev iteration you may rebuild the pipeline (different IDs, same names) and expect snapshots to carry over. Documented; the user controls naming.

6. **Empty-string segment names are ignored.** Stages outside any `Segment` have `segmentName == ""`; capture/replay applies ONLY to named segments. Pipelines with no segments are unchanged when `WithDevStore` is on.

7. **Nested segments are NOT captured independently when an outer segment is replayed.** Outer-segment replay short-circuits everything inside, including inner segments and their snapshots. The inner segment's snapshot, if it exists, is not consulted. This matches the spec's "segment is bypassed" wording.

8. **No type-safety check between segment output type and snapshot.** If a developer changes a segment's output type but doesn't delete the old snapshot, replay returns whatever JSON unmarshaling produces — probably zero values or partial fields. Documented as a known limitation; the user is responsible for invalidating snapshots after type changes.

---

## File structure

| File | Change |
|---|---|
| `devstore.go` (new) | `DevStore` interface, `FileDevStore` implementation, `WithDevStore` RunOption, capture/replay helpers used by `Segment.Apply` |
| `pipeline.go` | Add `devStore DevStore` to `runCtx` |
| `config.go` | Add `devStore DevStore` to `runConfig` |
| `kitsune.go` | Wire `cfg.devStore → rc.devStore` in `Runner.Run` |
| `segment.go` | Modify `Segment.Apply` to wrap its output's `build` closure with capture/replay logic when `rc.devStore` is set |
| `collect.go` (or new `checkpoint.go`) | `FromCheckpoint[T any](store, name, opts...) *Pipeline[T]` |
| `devstore_test.go` (new) | Unit tests: FileDevStore round-trip, missing key, file format |
| `segment_devstore_test.go` (new) | Integration tests: capture round-trip, replay-from-snapshot, replay-skips-effects, missing-segment-runs-live, empty-name-ignored, nested-segments |
| `checkpoint_test.go` (new) | FromCheckpoint tests: load existing, error on missing, end-to-end with DevStore |
| `examples/devstore/main.go` (new) | Self-contained demo: run twice, second run replays |
| `examples_test.go` | Register `"devstore"` example |
| `doc/operators.md` | Add DevStore + FromCheckpoint section |
| `doc/api-matrix.md` | Add rows |
| `doc/options.md` | Add `WithDevStore` RunOption entry |
| `doc/roadmap.md` | Mark M4 complete |
| Memory: `project_higher_level_authoring.md` | Update to reflect M4 shipped |

---

## Tasks

> Run `task test` after each task. Run `task test:race` before docs.

---

### Task 1: `DevStore` interface and `FileDevStore` implementation

Pure additive: new file, no integration with the rest of the package yet.

**Files:**
- Create: `devstore.go`
- Create: `devstore_test.go`

#### Step 1: Create `devstore.go`

Write `/Users/jonathan/projects/go-kitsune/devstore.go`:

```go
package kitsune

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sync"
)

// DevStore persists per-segment snapshots for development-time replay.
//
// It is a strictly development-only affordance. Snapshots may become stale
// silently when segment outputs change shape; there is no schema versioning,
// no concurrency safety beyond what implementations choose to provide, and
// no production guarantees. Attaching a DevStore to a production run is an
// explicit choice by the author.
//
// See [WithDevStore] for the run-level integration and [FromCheckpoint] for
// loading a snapshot as a pipeline source.
type DevStore interface {
    // Save persists items under segment as a JSON-array-shaped snapshot.
    // Implementations should overwrite any existing snapshot for segment.
    // The items slice is owned by the caller for the duration of Save and
    // should not be retained after Save returns.
    Save(ctx context.Context, segment string, items []json.RawMessage) error

    // Load returns the snapshot previously persisted under segment. If no
    // snapshot exists, Load returns ErrSnapshotMissing wrapped with the
    // segment name; callers should distinguish missing from other errors
    // via errors.Is.
    Load(ctx context.Context, segment string) ([]json.RawMessage, error)
}

// ErrSnapshotMissing is returned by [DevStore.Load] when no snapshot exists
// for the requested segment.
var ErrSnapshotMissing = fmt.Errorf("kitsune: devstore snapshot missing")

// FileDevStore writes one JSON file per segment under a configurable
// directory. The directory is created on first Save if it does not exist.
// A single FileDevStore is safe for concurrent Save and Load calls under
// distinct segment names; concurrent Save calls on the same segment have
// last-write-wins semantics.
type FileDevStore struct {
    dir string
    mu  sync.Mutex // serialises writes to a single file at a time
}

// NewFileDevStore returns a [FileDevStore] rooted at dir. The directory is
// created on first Save if it does not exist. dir is interpreted relative
// to the current working directory unless it is absolute.
func NewFileDevStore(dir string) *FileDevStore {
    return &FileDevStore{dir: dir}
}

// segmentPath returns the on-disk path for a segment's snapshot file.
func (s *FileDevStore) segmentPath(segment string) string {
    return filepath.Join(s.dir, segment+".json")
}

// Save persists items as a JSON array under segment.json in the store
// directory. The directory is created if it does not exist.
func (s *FileDevStore) Save(_ context.Context, segment string, items []json.RawMessage) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if err := os.MkdirAll(s.dir, 0o755); err != nil {
        return fmt.Errorf("kitsune: devstore mkdir %q: %w", s.dir, err)
    }
    data, err := json.Marshal(items)
    if err != nil {
        return fmt.Errorf("kitsune: devstore marshal segment %q: %w", segment, err)
    }
    if err := os.WriteFile(s.segmentPath(segment), data, 0o644); err != nil {
        return fmt.Errorf("kitsune: devstore write segment %q: %w", segment, err)
    }
    return nil
}

// Load returns the snapshot persisted under segment.json, or wraps
// [ErrSnapshotMissing] if the file does not exist.
func (s *FileDevStore) Load(_ context.Context, segment string) ([]json.RawMessage, error) {
    data, err := os.ReadFile(s.segmentPath(segment))
    if err != nil {
        if os.IsNotExist(err) {
            return nil, fmt.Errorf("%w: segment %q", ErrSnapshotMissing, segment)
        }
        return nil, fmt.Errorf("kitsune: devstore read segment %q: %w", segment, err)
    }
    var items []json.RawMessage
    if err := json.Unmarshal(data, &items); err != nil {
        return nil, fmt.Errorf("kitsune: devstore unmarshal segment %q: %w", segment, err)
    }
    return items, nil
}
```

#### Step 2: Create `devstore_test.go`

Write `/Users/jonathan/projects/go-kitsune/devstore_test.go`:

```go
package kitsune_test

import (
    "context"
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "testing"

    "github.com/zenbaku/go-kitsune"
)

// TestFileDevStore_RoundTrip verifies that items saved under a segment are
// returned verbatim by a subsequent Load.
func TestFileDevStore_RoundTrip(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)
    ctx := context.Background()

    items := []json.RawMessage{
        json.RawMessage(`{"id":1}`),
        json.RawMessage(`{"id":2}`),
        json.RawMessage(`{"id":3}`),
    }
    if err := store.Save(ctx, "seg", items); err != nil {
        t.Fatalf("Save: %v", err)
    }
    got, err := store.Load(ctx, "seg")
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if len(got) != len(items) {
        t.Fatalf("got %d items, want %d", len(got), len(items))
    }
    for i, raw := range got {
        if string(raw) != string(items[i]) {
            t.Errorf("item %d: got %q, want %q", i, raw, items[i])
        }
    }
}

// TestFileDevStore_LoadMissing verifies that Load on an unknown segment
// returns ErrSnapshotMissing wrapping the segment name.
func TestFileDevStore_LoadMissing(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)
    _, err := store.Load(context.Background(), "absent")
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    if !errors.Is(err, kitsune.ErrSnapshotMissing) {
        t.Errorf("err=%v, want errors.Is(err, ErrSnapshotMissing)", err)
    }
    if !contains(err.Error(), "absent") {
        t.Errorf("err=%v, want to contain segment name", err)
    }
}

// TestFileDevStore_CreatesDir verifies that Save creates the directory if
// it does not exist.
func TestFileDevStore_CreatesDir(t *testing.T) {
    parent := t.TempDir()
    nested := filepath.Join(parent, "deeply", "nested")
    store := kitsune.NewFileDevStore(nested)
    if err := store.Save(context.Background(), "x", []json.RawMessage{json.RawMessage(`1`)}); err != nil {
        t.Fatalf("Save: %v", err)
    }
    if _, err := os.Stat(filepath.Join(nested, "x.json")); err != nil {
        t.Errorf("expected x.json to exist: %v", err)
    }
}

// TestFileDevStore_Overwrite verifies that re-Saving the same segment
// replaces the previous snapshot.
func TestFileDevStore_Overwrite(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)
    ctx := context.Background()

    if err := store.Save(ctx, "seg", []json.RawMessage{json.RawMessage(`1`)}); err != nil {
        t.Fatal(err)
    }
    if err := store.Save(ctx, "seg", []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`3`)}); err != nil {
        t.Fatal(err)
    }
    got, err := store.Load(ctx, "seg")
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 2 || string(got[0]) != "2" || string(got[1]) != "3" {
        t.Errorf("got %v, want [2 3]", got)
    }
}

func contains(s, sub string) bool {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return true
        }
    }
    return false
}
```

#### Step 3: Run the tests

Run: `go test -run "TestFileDevStore_" ./...`
Expected: 4 PASS.

#### Step 4: Run the full short suite

Run: `task test`
Expected: PASS.

#### Step 5: Commit

```bash
git add devstore.go devstore_test.go
git commit -m "feat(devstore): add DevStore interface and FileDevStore"
```

---

### Task 2: Plumb `DevStore` through `runConfig` / `runCtx`; add `WithDevStore` RunOption

Pure plumbing: add the field, expose the option. No behaviour wired up yet.

**Files:**
- Modify: `config.go`
- Modify: `pipeline.go`
- Modify: `kitsune.go`

#### Step 1: Add `devStore` to `runConfig`

In `/Users/jonathan/projects/go-kitsune/config.go`, locate `runConfig` (around line 65). Add the field after `dryRun`:

```go
type runConfig struct {
    // ... existing fields unchanged ...
    dryRun              bool
    devStore            DevStore
}
```

#### Step 2: Add `devStore` to `runCtx`

In `/Users/jonathan/projects/go-kitsune/pipeline.go`, locate `runCtx` (around line 114). Add the field after `dryRun` (and after `effectStats` if present from M3):

```go
type runCtx struct {
    // ... existing fields unchanged through effectStats ...
    effectStats map[int64]*effectStat

    // devStore, if non-nil, enables per-Segment snapshot capture and replay.
    // Set from cfg.devStore by Runner.Run. Read by Segment.Apply during the
    // build phase to wrap segment output with capture/replay logic.
    devStore DevStore
}
```

#### Step 3: Add `WithDevStore` RunOption

In `/Users/jonathan/projects/go-kitsune/config.go`, locate the existing RunOptions (e.g. `WithDefaultKeyTTL`, `DryRun`). Append a new RunOption immediately after `DryRun`:

```go
// WithDevStore configures the run to use a [DevStore] for per-Segment
// snapshot capture and replay. On each segment in the pipeline:
//   - if the store has a snapshot under the segment's name, the snapshot is
//     replayed as the segment's output and the inner stages are bypassed
//   - otherwise, the segment runs live and its output is captured to the
//     store at segment exit
//
// Snapshots become stale silently when segment outputs change shape; delete
// the snapshot file (or use a fresh store directory) to force a re-run.
//
// DevStore is a development-time affordance with no production safety.
// See [DevStore] for full semantics.
func WithDevStore(store DevStore) RunOption {
    return func(c *runConfig) { c.devStore = store }
}
```

#### Step 4: Wire `cfg.devStore → rc.devStore` in `Runner.Run`

In `/Users/jonathan/projects/go-kitsune/kitsune.go`, locate `Runner.Run` (the body that copies cfg fields into rc, around the lines that set `rc.dryRun = cfg.dryRun`). Add the devStore line:

```go
rc.dryRun = cfg.dryRun
rc.devStore = cfg.devStore
r.terminal(rc)
```

#### Step 5: Build to verify

Run: `go build ./...`
Expected: clean.

#### Step 6: Run the tests

Run: `task test`
Expected: PASS — additive plumbing with no readers yet.

#### Step 7: Commit

```bash
git add config.go pipeline.go kitsune.go
git commit -m "feat(devstore): plumb DevStore through runConfig and runCtx"
```

---

### Task 3: Implement `FromCheckpoint[T]` source

A test-time / dev-time source that loads from a `DevStore`. Independent of the rest of M4 — useful even without `WithDevStore`.

**Files:**
- Create: `checkpoint.go`
- Create: `checkpoint_test.go`

#### Step 1: Create `checkpoint.go`

Write `/Users/jonathan/projects/go-kitsune/checkpoint.go`. Mirror the structure of `FromSlice` in `source.go` (don't copy `Generate` because it doesn't accept stage options).

```go
package kitsune

import (
    "context"
    "encoding/json"
    "fmt"
)

// FromCheckpoint creates a [Pipeline] that emits items previously persisted
// under segment in store. It is the test-time companion to [WithDevStore]:
// load a stored snapshot directly as a source, without re-running the
// pipeline that produced it.
//
// Items are unmarshaled using encoding/json. The element type T must be
// JSON-compatible with whatever was stored. If the snapshot is missing,
// Run returns an error wrapping [ErrSnapshotMissing]. Unmarshal errors
// abort the source.
//
//	store := kitsune.NewFileDevStore("testdata/checkpoints")
//	p := kitsune.FromCheckpoint[EnrichedPage](store, "enrich-pages")
//	// Use p as the input to a downstream stage under test.
//
// Stage options like [WithName] and [Buffer] are honoured; concurrency
// options have no effect on a source.
func FromCheckpoint[T any](store DevStore, segment string, opts ...StageOption) *Pipeline[T] {
    cfg := buildStageConfig(opts)
    id := nextPipelineID()
    meta := stageMeta{
        id:          id,
        kind:        "source",
        name:        orDefault(cfg.name, "from_checkpoint"),
        concurrency: 1,
        buffer:      cfg.buffer,
    }
    var out *Pipeline[T]
    build := func(rc *runCtx) chan T {
        if existing := rc.getChan(id); existing != nil {
            return existing.(chan T)
        }
        buf := rc.effectiveBufSize(cfg)
        ch := make(chan T, buf)
        m := meta
        m.buffer = buf
        m.getChanLen = func() int { return len(ch) }
        m.getChanCap = func() int { return cap(ch) }
        rc.setChan(id, ch)
        rc.initDrainNotify(id, out.consumerCount.Load())
        drainCh := rc.drainCh(id)
        gate := rc.gate

        stage := sourceStage(ch, gate, drainCh, func(ctx context.Context, send func(T) error) error {
            raw, err := store.Load(ctx, segment)
            if err != nil {
                return fmt.Errorf("kitsune: from_checkpoint %q: %w", segment, err)
            }
            for i, data := range raw {
                var item T
                if err := json.Unmarshal(data, &item); err != nil {
                    return fmt.Errorf("kitsune: from_checkpoint %q item %d: %w", segment, i, err)
                }
                if err := send(item); err != nil {
                    return err
                }
            }
            return nil
        })

        rc.add(stage, m)
        return ch
    }
    out = newPipeline(id, meta, build)
    return out
}
```

`sourceStage` is the existing helper in `source.go` (around lines 24-49); reuse it. Since `Gate` is a type alias for `internal.Gate` and `sourceStage` accepts a `*internal.Gate`, passing `rc.gate` works without importing `internal` here.

#### Step 2: Create `checkpoint_test.go`

Write `/Users/jonathan/projects/go-kitsune/checkpoint_test.go`:

```go
package kitsune_test

import (
    "context"
    "encoding/json"
    "errors"
    "reflect"
    "testing"

    "github.com/zenbaku/go-kitsune"
)

type checkpointItem struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}

// TestFromCheckpoint_LoadsStoredSnapshot verifies that FromCheckpoint emits
// the items previously persisted by Save.
func TestFromCheckpoint_LoadsStoredSnapshot(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)

    raws := []json.RawMessage{
        json.RawMessage(`{"id":1,"name":"a"}`),
        json.RawMessage(`{"id":2,"name":"b"}`),
    }
    if err := store.Save(context.Background(), "items", raws); err != nil {
        t.Fatal(err)
    }

    p := kitsune.FromCheckpoint[checkpointItem](store, "items")
    got, err := kitsune.Collect(context.Background(), p)
    if err != nil {
        t.Fatal(err)
    }
    want := []checkpointItem{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
    if !reflect.DeepEqual(got, want) {
        t.Errorf("got %+v, want %+v", got, want)
    }
}

// TestFromCheckpoint_MissingErrors verifies that running FromCheckpoint with
// no stored snapshot returns an error wrapping ErrSnapshotMissing.
func TestFromCheckpoint_MissingErrors(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)

    p := kitsune.FromCheckpoint[checkpointItem](store, "absent")
    _, err := kitsune.Collect(context.Background(), p)
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    if !errors.Is(err, kitsune.ErrSnapshotMissing) {
        t.Errorf("err=%v, want errors.Is(err, ErrSnapshotMissing)", err)
    }
}

// TestFromCheckpoint_WithName verifies that WithName is honoured by
// FromCheckpoint's stage metadata.
func TestFromCheckpoint_WithName(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)
    if err := store.Save(context.Background(), "x", []json.RawMessage{json.RawMessage(`0`)}); err != nil {
        t.Fatal(err)
    }
    p := kitsune.FromCheckpoint[int](store, "x", kitsune.WithName("custom"))
    nodes := p.Describe()
    var found bool
    for _, n := range nodes {
        if n.Name == "custom" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected stage named %q in graph, got %+v", "custom", nodes)
    }
}
```

#### Step 3: Run the tests

Run: `go test -run "TestFromCheckpoint_" ./...`
Expected: 3 PASS.

#### Step 4: Run the full short suite

Run: `task test`
Expected: PASS.

#### Step 5: Commit

```bash
git add checkpoint.go checkpoint_test.go
git commit -m "feat(devstore): add FromCheckpoint source"
```

---

### Task 4: Wrap `Segment.Apply` with capture/replay

This is the integration task. When `rc.devStore` is set during the build phase, the Segment's output `build` closure does one of:
- Replay: call `store.Load(name)`; on hit, allocate a fresh output channel, spawn a goroutine that unmarshals snapshot items and emits them, skip calling the inner stage's build (so its stages never register in `rc.stages`).
- Capture: call the inner stage's build, then spawn a tee goroutine that reads the inner output, marshals each item into a buffer, forwards to a fresh output channel, and at end-of-input calls `store.Save(name, buffer)`.
- No-op: if `rc.devStore` is nil OR segment name is empty, behave exactly as before.

The current `Segment.Apply` (in `/Users/jonathan/projects/go-kitsune/segment.go`) wraps `output.build` to populate `rc.segmentByID`. The wrapper composes: outer wraps inner. We need to extend that wrapper to also handle DevStore.

**Files:**
- Modify: `segment.go`

#### Step 1: Read the current Segment.Apply

Open `/Users/jonathan/projects/go-kitsune/segment.go`. The current `Apply` (lines 57-84) is:

```go
func (s Segment[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
    before := atomic.LoadInt64(&globalIDSeq)
    output := s.stage.Apply(p)
    after := atomic.LoadInt64(&globalIDSeq)

    if after <= before {
        return output
    }
    ids := make([]int64, 0, after-before)
    for id := before + 1; id <= after; id++ {
        ids = append(ids, id)
    }

    name := s.name
    originalBuild := output.build
    output.build = func(rc *runCtx) chan O {
        for _, id := range ids {
            rc.segmentByID[id] = name
        }
        return originalBuild(rc)
    }
    return output
}
```

#### Step 2: Extend the wrapper

Replace the `Apply` method body with:

```go
func (s Segment[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
    before := atomic.LoadInt64(&globalIDSeq)
    output := s.stage.Apply(p)
    after := atomic.LoadInt64(&globalIDSeq)

    if after <= before {
        return output
    }
    ids := make([]int64, 0, after-before)
    for id := before + 1; id <= after; id++ {
        ids = append(ids, id)
    }

    name := s.name
    originalBuild := output.build
    output.build = func(rc *runCtx) chan O {
        // Always stamp segment IDs first; this is independent of DevStore.
        for _, id := range ids {
            rc.segmentByID[id] = name
        }

        // DevStore branch: only when a store is attached AND the segment has
        // a non-empty name.
        if rc.devStore != nil && name != "" {
            if replayed, ok := tryReplaySegment[O](rc, name); ok {
                return replayed
            }
            return captureSegment[O](rc, name, originalBuild(rc))
        }
        return originalBuild(rc)
    }
    return output
}
```

#### Step 3: Add `tryReplaySegment` and `captureSegment` helpers

Append to `/Users/jonathan/projects/go-kitsune/segment.go`:

```go
// tryReplaySegment attempts to load a snapshot for name from rc.devStore.
// On success it returns a fresh output channel of type T and true; a
// goroutine drains the snapshot into the channel asynchronously.
// On miss (ErrSnapshotMissing) it returns nil, false; the caller should
// fall back to capture mode.
//
// This function does not register a stage in rc.stages: the spawned
// goroutine runs out-of-band. That is appropriate because DevStore is a
// dev-only affordance and the goroutine is short-lived (bounded by snapshot
// size, no upstream waiting).
func tryReplaySegment[T any](rc *runCtx, name string) (chan T, bool) {
    raws, err := rc.devStore.Load(context.Background(), name)
    if err != nil {
        // Missing snapshot is the common case; non-missing errors are
        // dev-only and fall through to capture mode (run live).
        return nil, false
    }
    ch := make(chan T, 16)
    go func() {
        defer close(ch)
        for _, data := range raws {
            var item T
            if err := json.Unmarshal(data, &item); err != nil {
                // Best-effort: stop emission, leave channel closed. A future
                // hook could surface this to the user.
                return
            }
            ch <- item
        }
    }()
    return ch, true
}

// captureSegment wraps an upstream output channel with a tee goroutine that
// marshals each item into a buffer, forwards items downstream, and calls
// store.Save at end of input.
//
// Like tryReplaySegment, this runs out-of-band of the rc.stages graph.
// Save errors are silently dropped: capture is best-effort and a save failure
// should not escalate into a run failure.
func captureSegment[T any](rc *runCtx, name string, in chan T) chan T {
    out := make(chan T, cap(in))
    go func() {
        defer close(out)
        var buf []json.RawMessage
        for item := range in {
            data, err := json.Marshal(item)
            if err == nil {
                buf = append(buf, data)
            }
            out <- item
        }
        // Save once at end-of-input. Best-effort: errors are dropped.
        _ = rc.devStore.Save(context.Background(), name, buf)
    }()
    return out
}
```

You will need to add imports for `context` and `encoding/json` to segment.go. The current segment.go imports `sync/atomic`; add the others:

```go
import (
    "context"
    "encoding/json"
    "sync/atomic"
)
```

#### Step 4: Build to verify

Run: `go build ./...`
Expected: clean.

#### Step 5: Run existing tests

Run: `task test`
Expected: PASS — existing segment tests don't attach a DevStore, so the new branch is never taken; behaviour is unchanged.

#### Step 6: Commit

```bash
git add segment.go
git commit -m "feat(devstore): wire Segment.Apply to capture or replay when DevStore is attached"
```

---

### Task 5: Integration tests for capture/replay

Verify the round-trip end to end through the public API.

**Files:**
- Create: `segment_devstore_test.go`

#### Step 1: Create the test file

Write `/Users/jonathan/projects/go-kitsune/segment_devstore_test.go`:

```go
package kitsune_test

import (
    "context"
    "encoding/json"
    "reflect"
    "sync/atomic"
    "testing"

    "github.com/zenbaku/go-kitsune"
)

// TestSegmentDevStore_CaptureThenReplay verifies the round-trip: first run
// captures, second run replays without invoking the inner stage.
func TestSegmentDevStore_CaptureThenReplay(t *testing.T) {
    dir := t.TempDir()
    store := kitsune.NewFileDevStore(dir)
    ctx := context.Background()

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
        out, err := kitsune.CollectWith(ctx, seg.Apply(src), kitsune.WithDevStore(store))
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
//
// Empty-named segments should not be possible via the public API (NewSegment
// requires a name argument), but if a Segment value with an empty name is
// constructed via the zero value (Segment[I, O]{}), DevStore must skip it
// rather than silently saving snapshots under "".
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
    ctx := context.Background()

    var sideEffects atomic.Int32
    inner := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
        return kitsune.Map(p, func(_ context.Context, v int) (int, error) {
            sideEffects.Add(1)
            return v, nil
        })
    })
    seg := kitsune.NewSegment("inner-effects", inner)

    // First run: live, side effects fire.
    if _, err := kitsune.CollectWith(ctx, seg.Apply(kitsune.FromSlice([]int{1, 2})), kitsune.WithDevStore(store)); err != nil {
        t.Fatal(err)
    }
    firstRunCalls := sideEffects.Load()
    if firstRunCalls != 2 {
        t.Fatalf("first run side-effects = %d, want 2", firstRunCalls)
    }

    // Second run: replay; side effects do NOT fire.
    if _, err := kitsune.CollectWith(ctx, seg.Apply(kitsune.FromSlice([]int{1, 2})), kitsune.WithDevStore(store)); err != nil {
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
    if _, err := kitsune.CollectWith(ctx, seg.Apply(src), kitsune.WithDevStore(store)); err != nil {
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
```

#### Step 2: Verify `CollectWith` exists or use an inline equivalent

`CollectWith` may not exist as a helper in this codebase; the existing pattern is `kitsune.Collect(ctx, p)` with no opts. To pass `WithDevStore`, callers typically need a Runner.

Check by running:

```bash
grep -n "^func CollectWith" /Users/jonathan/projects/go-kitsune/collect.go
```

If `CollectWith` does not exist, replace each `kitsune.CollectWith(ctx, p, opts...)` call in the test with the explicit Runner pattern:

```go
var got []int
_, err := p.ForEach(func(_ context.Context, v int) error {
    got = append(got, v)
    return nil
}).Run(ctx, opts...)
```

Or define a local test helper:

```go
func collectWith[T any](t *testing.T, p *kitsune.Pipeline[T], opts ...kitsune.RunOption) ([]T, error) {
    var out []T
    _, err := p.ForEach(func(_ context.Context, item T) error {
        out = append(out, item)
        return nil
    }).Run(context.Background(), opts...)
    return out, err
}
```

Use whichever fits the existing codebase convention; `runWith` from `errorstrategy_test.go` is the existing helper but it doesn't take RunOptions in the way needed here. Define a local `collectWith` helper at the top of `segment_devstore_test.go` if needed.

#### Step 3: Run the tests

Run: `go test -run "TestSegmentDevStore_" ./... -race -v`
Expected: 5 PASS, no races.

If any test fails, investigate before continuing — these tests check real semantics. Likely failures:
- `CaptureThenReplay` failing on the second run with `liveCalls != 0` means the replay path isn't bypassing the inner stage correctly (Task 4 bug).
- `FormatPreservesItems` failing the JSON unmarshal means the capture goroutine isn't using `json.Marshal` correctly.

#### Step 4: Run the full short suite

Run: `task test`
Expected: PASS.

#### Step 5: Commit

```bash
git add segment_devstore_test.go
git commit -m "test(devstore): add Segment + DevStore capture/replay integration tests"
```

---

### Task 6: Add the example

Self-contained demo that runs the same pipeline twice and shows the second run replaying.

**Files:**
- Create: `examples/devstore/main.go`
- Modify: `examples_test.go`

#### Step 1: Create the example

Write `/Users/jonathan/projects/go-kitsune/examples/devstore/main.go`:

```go
// Example: DevStore captures each Segment's output on the first run and
// replays from snapshot on subsequent runs, letting you iterate on a
// downstream segment without re-running expensive upstream work.
//
//	go run ./examples/devstore
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/zenbaku/go-kitsune"
)

type Page struct {
    URL  string
    Body string
}

func main() {
    dir, err := os.MkdirTemp("", "kitsune-devstore-")
    if err != nil {
        panic(err)
    }
    defer os.RemoveAll(dir)
    store := kitsune.NewFileDevStore(dir)

    fetch := kitsune.Stage[string, Page](func(urls *kitsune.Pipeline[string]) *kitsune.Pipeline[Page] {
        return kitsune.Map(urls, func(_ context.Context, url string) (Page, error) {
            fmt.Printf("  fetch  %s\n", url)
            return Page{URL: url, Body: "body of " + url}, nil
        })
    })
    enrich := kitsune.Stage[Page, Page](func(p *kitsune.Pipeline[Page]) *kitsune.Pipeline[Page] {
        return kitsune.Map(p, func(_ context.Context, page Page) (Page, error) {
            fmt.Printf("  enrich %s\n", page.URL)
            page.Body = page.Body + " (enriched)"
            return page, nil
        })
    })

    fetchSeg := kitsune.NewSegment("fetch", fetch)
    enrichSeg := kitsune.NewSegment("enrich", enrich)

    runOnce := func(label string) {
        fmt.Printf("--- %s ---\n", label)
        urls := kitsune.FromSlice([]string{"https://a", "https://b"})
        pipeline := kitsune.Then(fetchSeg, enrichSeg)
        runner := pipeline.Apply(urls).
            ForEach(func(_ context.Context, page Page) error {
                fmt.Printf("  result %s -> %s\n", page.URL, page.Body)
                return nil
            })
        if _, err := runner.Run(context.Background(), kitsune.WithDevStore(store)); err != nil {
            panic(err)
        }
    }

    runOnce("first run (captures snapshots)")
    fmt.Println()
    runOnce("second run (replays snapshots; no fetch/enrich logs)")
}
```

#### Step 2: Register the example

Open `/Users/jonathan/projects/go-kitsune/examples_test.go`. Add `"devstore"` to the `examples` slice in alphabetical order. The slice currently contains entries like `"contextmapper"`, `"effect"`, `"enrich"` ... insert `"devstore"` between `"contextmapper"` and `"effect"` (or wherever alphabetical order lands it):

```go
"contextmapper",
"devstore",
"effect",
```

#### Step 3: Run the example

Run: `go run ./examples/devstore`

Expected output: first run prints `fetch` and `enrich` lines for each URL plus `result` lines; second run prints only `result` lines (no `fetch`/`enrich` because the snapshot is replayed).

Exact text doesn't matter; the absence of `fetch`/`enrich` lines on the second run is the critical signal.

#### Step 4: Run the example smoke test

Run: `go test -run TestExamples/devstore -timeout 60s .`
Expected: PASS.

#### Step 5: Run the full short suite

Run: `task test`
Expected: PASS.

#### Step 6: Commit

```bash
git add examples/devstore/main.go examples_test.go
git commit -m "test(devstore): add example demonstrating capture and replay"
```

---

### Task 7: Documentation

Add user-facing reference for `DevStore`, `WithDevStore`, and `FromCheckpoint`.

**Files:**
- Modify: `doc/operators.md`
- Modify: `doc/api-matrix.md`
- Modify: `doc/options.md`
- Modify: `doc/roadmap.md`

#### Step 1: Add a "DevStore" section to `doc/operators.md`

Locate the "Run Summary" section (added in M3). After its closing `---` separator, insert a new top-level section before "Error Handling Options":

````markdown
## :material-floppy-variant: DevStore { #devstore }

`DevStore` captures and replays per-`Segment` snapshots so you can iterate on a downstream segment without re-running expensive upstream work. It is strictly a development-time affordance: no schema versioning, no production safety. See the safety note below.

### DevStore interface

```go
type DevStore interface {
    Save(ctx context.Context, segment string, items []json.RawMessage) error
    Load(ctx context.Context, segment string) ([]json.RawMessage, error)
}

var ErrSnapshotMissing error // wrapped by Load when no snapshot exists
```

`FileDevStore` is the built-in implementation, writing one JSON file per segment under a configurable directory:

```go
func NewFileDevStore(dir string) *FileDevStore
```

### WithDevStore

```go
func WithDevStore(store DevStore) RunOption
```

When attached to a run, every named [`Segment`](#segment) in the pipeline gets capture-or-replay behaviour:

- **Snapshot exists for the segment's name:** the inner stages are bypassed entirely (including any [`Effect`](#effect) calls inside) and the snapshot is replayed as the segment's output.
- **Snapshot missing:** the segment runs live and its output is captured to the store at segment exit.

Empty-named segments (the zero `Segment[I, O]{}` value) are ignored.

To force a segment to re-run after its output type or logic changes, delete its snapshot file from the store directory.

### FromCheckpoint

```go
func FromCheckpoint[T any](store DevStore, segment string, opts ...StageOption) *Pipeline[T]
```

A test-time / dev-time source that loads a stored snapshot directly. Use it to drive a pipeline section under test from a frozen upstream output without re-running:

```go
store := kitsune.NewFileDevStore("testdata/checkpoints")
p := kitsune.FromCheckpoint[EnrichedPage](store, "enrich")
out, err := kitsune.Collect(ctx, kitsune.Map(p, processEnriched))
```

If the snapshot does not exist, `Run` returns an error wrapping `ErrSnapshotMissing`. Unmarshal failures abort the source.

Items are unmarshaled with `encoding/json` directly; `T` must be JSON-compatible with whatever was stored.

### Safety

`DevStore` is a development-only affordance. It is intentionally not production-safe:

- Snapshots may become stale silently when segment outputs change shape.
- Serialization is JSON-based and may lose type fidelity for complex types.
- There is no version, schema, or hash tracking.
- Capture is best-effort; `Save` errors are silently dropped so a broken store does not fail the run.

Attaching a `DevStore` in a production run is an explicit choice by the author. The recommended use is gated behind a build tag, environment variable, or explicit dev script.

### Example

```go
store := kitsune.NewFileDevStore("/tmp/kitsune-snapshots")

fetchSeg := kitsune.NewSegment("fetch", fetchStage)
enrichSeg := kitsune.NewSegment("enrich", enrichStage)
publishSeg := kitsune.NewSegment("publish", publishStage)

pipeline := kitsune.Then(kitsune.Then(fetchSeg, enrichSeg), publishSeg)

// First run: everything live, snapshots captured.
runner := pipeline.Apply(src).ForEach(handle)
if _, err := runner.Run(ctx, kitsune.WithDevStore(store)); err != nil {
    log.Fatal(err)
}

// Iterate on the publish segment: delete its snapshot, leave fetch/enrich
// snapshots in place. Second run replays fetch and enrich; runs publish live.
os.Remove("/tmp/kitsune-snapshots/publish.json")
if _, err := runner.Run(ctx, kitsune.WithDevStore(store)); err != nil {
    log.Fatal(err)
}
```

---
````

#### Step 2: Add api-matrix entries

Open `/Users/jonathan/projects/go-kitsune/doc/api-matrix.md`. After section 12.7 Run Summary, add a new section 12.8 DevStore:

```markdown
## 12.8 · DevStore

| Symbol | Signature | Notes |
|--------|-----------|-------|
| `DevStore` | `interface { Save(ctx, name, []json.RawMessage); Load(ctx, name) ([]json.RawMessage, error) }` | Per-segment snapshot store; dev-only |
| `ErrSnapshotMissing` | `var ErrSnapshotMissing error` | Wrapped by `Load` for unknown segments; check via `errors.Is` |
| `FileDevStore` | `type FileDevStore struct{ ... }` | Built-in; one JSON file per segment |
| `NewFileDevStore` | `NewFileDevStore(dir string) *FileDevStore` | Directory created on first Save |
| `FromCheckpoint` | `FromCheckpoint[T](store, segment, opts...) *Pipeline[T]` | Test-time source loading a stored snapshot |

---
```

In section 16 · Run Options, append a row:

```markdown
| `WithDevStore` | `WithDevStore(store DevStore)` | Per-Segment capture/replay; dev-only. See [`DevStore`](operators.md#devstore). |
```

#### Step 3: Add `WithDevStore` to `doc/options.md`

Open `/Users/jonathan/projects/go-kitsune/doc/options.md`. Append a new entry at the end (matching the file's existing `## OptionName(args)` format):

````markdown
## `WithDevStore(store DevStore)`

```go
func WithDevStore(store DevStore) RunOption
```

When attached to a run, every named [`Segment`](operators.md#segment) gets per-segment snapshot capture/replay against the supplied [`DevStore`](operators.md#devstore). Snapshot exists → segment is bypassed (inner stages including effects do not run) and the snapshot is replayed; snapshot missing → segment runs live and its output is captured.

Strictly dev-only: no schema versioning, no production safety. See [DevStore](operators.md#devstore).

```go
store := kitsune.NewFileDevStore("/tmp/snaps")
runner.Run(ctx, kitsune.WithDevStore(store))
```
````

#### Step 4: Mark M4 complete in `doc/roadmap.md`

Locate the M4 bullet (around line 97). Replace its prose with:

```markdown
    - **M4 — `DevStore` + `FromCheckpoint`.** *(shipped 2026-04-25.)* `DevStore` interface (`Save` / `Load([]json.RawMessage)` plus `ErrSnapshotMissing`), `FileDevStore` writing one JSON file per segment. `WithDevStore(store) RunOption` enables per-`Segment` capture/replay: snapshot present → segment is bypassed; snapshot missing → segment runs live and is captured. `FromCheckpoint[T](store, name)` is a test-time source loading a stored snapshot directly. Strictly dev-only; no schema versioning. `ReplayThrough` was deferred (semantics underspecified for branched DAGs; manual snapshot file deletion covers the pragmatic use case).
```

If the roadmap has a "Higher-level authoring layer M2-M4" wrapper bullet that's still marked open (`- [ ]`), update it to reflect that all four milestones have shipped: change the `[ ]` to `[x]` and adjust the heading prose accordingly.

#### Step 5: Run docs sanity grep

```bash
grep -rn "DevStore\|FromCheckpoint" doc/ | head -20
```

Spot-check that the cross-references are consistent (anchors like `#devstore`, `#segment`, `#effect` exist in operators.md).

#### Step 6: Run all tests once more

```bash
task test && task test:race
```
Expected: PASS.

#### Step 7: Commit

```bash
git add doc/operators.md doc/api-matrix.md doc/options.md doc/roadmap.md
git commit -m "docs(devstore): add DevStore, WithDevStore, FromCheckpoint reference"
```

---

### Task 8: Memory + final verification

**Files:**
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/project_higher_level_authoring.md`
- Modify: `~/.claude/projects/-Users-jonathan-projects-go-kitsune/memory/MEMORY.md`

#### Step 1: Update higher-level-authoring memory

Replace the M4 "(next)" stanza with a "(shipped 2026-04-25)" version describing the actual shipped surface, and remove the M4-implementation hints (they are no longer needed once M4 ships).

The replacement bullet:

```markdown
**M4 — DevStore + FromCheckpoint (shipped 2026-04-25).** `DevStore` interface (`Save(ctx, segment, []json.RawMessage)`, `Load(ctx, segment) ([]json.RawMessage, error)`, with `ErrSnapshotMissing` for unknown segments). `FileDevStore` writes one JSON file per segment under a directory. `WithDevStore(store) RunOption` enables per-`Segment` capture/replay: snapshot present → segment is bypassed (inner stages including any Effects do not run) and snapshot is replayed as output; snapshot missing → segment runs live and output is captured. `FromCheckpoint[T any](store, segment, opts...) *Pipeline[T]` is a test-time source. Empty-named segments and unattached runs are no-ops. Strictly dev-only: no schema versioning. `ReplayThrough` was deferred from v1 (semantics underspecified for branched DAGs; manual snapshot file deletion covers iteration). Plan at `docs/superpowers/plans/2026-04-25-higher-level-authoring-devstore.md`.
```

Also remove or adjust any "How to apply (M4)" guidance that was written for the not-yet-shipped milestone.

If the memory file's frontmatter description still says "M4 (DevStore) remains", update it to say "All four milestones shipped" or similar.

#### Step 2: Update MEMORY.md index summary

Reflect that all four milestones are complete:

```markdown
| `project_higher_level_authoring.md` | Higher-level authoring layer (Segment / Effect / RunSummary / DevStore): all four milestones shipped 2026-04-24 / 2026-04-25 |
```

#### Step 3: Final test pass

```bash
task test
task test:race
task test:property
task test:examples
```

All four must PASS. (`task test:ext` retains the pre-existing `GOWORK` configuration issue and is not in scope here.)

#### Step 4: Inform the user

Concise report: M4 commit count, gates passed, and that the higher-level authoring layer is now complete.

---

## Self-review notes (for the plan author, not the engineer)

- **Spec coverage:** every M4 surface from the spec is mapped to a task: `DevStore` interface (Task 1), `FileDevStore` (Task 1), `WithDevStore` (Tasks 2, 4), capture/replay semantics (Tasks 4, 5), `FromCheckpoint[T]` (Task 3). Spec deltas (drop `ReplayThrough` for v1, capture is best-effort, JSON-direct unmarshal in `FromCheckpoint`, empty-name skip) are documented up front.
- **Type-name consistency:** `DevStore`, `FileDevStore`, `NewFileDevStore`, `ErrSnapshotMissing`, `WithDevStore`, `FromCheckpoint`, `tryReplaySegment`, `captureSegment`, `runConfig.devStore`, `runCtx.devStore` are used consistently.
- **No placeholders:** every code step shows the actual content. The `CollectWith` helper in Task 5 is conditional on whether the helper exists; Task 5 step 2 explicitly tells the engineer to verify and substitute a local helper if not.
- **Sequencing:** Task 4 is the integration task; Tasks 1-3 are independently shippable. If Task 4 reveals a design issue, Tasks 1-3 can stand alone (just the interface, store, and source — no Segment integration). Task 5 verifies Task 4's correctness through the public API.
- **Deferred items:** `ReplayThrough` is explicitly deferred and documented in the spec-deltas section. No partial implementation; a follow-up plan can add it.
- **Frequent commits:** 8 task commits.
