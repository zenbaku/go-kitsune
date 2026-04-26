# Dev iteration with DevStore

`DevStore` is a development-time affordance that lets you iterate on a downstream segment of your pipeline without re-running expensive upstream work. It captures each named [`Segment`](operators.md#segment)'s output to disk on the first run, and replays from those snapshots on subsequent runs.

This page is the hands-on guide. For the type-by-type reference, see [DevStore in the operator catalog](operators.md#devstore).

---

## Mental model

A pipeline with `Segment`s is a topological chain of named groups:

```
                  fetch              enrich            publish
   urls  --------------> Pages --------------> Pages -----------> Acks
              segment              segment             segment
```

When you attach a [`WithDevStore(store)`](options.md#withdevstorestore-devstore) RunOption, each segment becomes a **snapshot boundary**:

- **First run** with the store empty: every segment runs live; on completion each segment's output is written to disk under `<dir>/<segment-name>.json`.
- **Subsequent runs** with snapshots present: each segment is bypassed entirely. Its inner stages do not run. Its stored output is replayed downstream as if it had just been computed.
- **Mixed**: any segment whose snapshot file is missing runs live (and is captured fresh). Any whose snapshot exists is replayed.

Snapshots are keyed by segment **name**, not by segment ID. That is intentional: rebuilding the pipeline (different IDs, same names) keeps your snapshots valid.

When the [Inspector](inspector.md) is attached to a run that replays a snapshot, the replayed segment renders as a single node with the **REPLAY** badge in the segment hull. This makes capture and replay visually distinguishable on the dashboard.

---

## The basic workflow

### 1. Wrap each major stage group in a `Segment`

```go
fetch   := kitsune.NewSegment("fetch",   fetchStage)
enrich  := kitsune.NewSegment("enrich",  enrichStage)
publish := kitsune.NewSegment("publish", publishStage)

pipeline := kitsune.Then(kitsune.Then(fetch, enrich), publish)
```

If your stages are not yet wrapped in `Segment`s, this is a one-time refactor. The wrappers are pure metadata in production (when no `WithDevStore` is attached, segments behave identically to plain stages, with one exception: see [Fusion](#interaction-with-fusion) below).

### 2. Attach a `DevStore` when iterating

```go
store := kitsune.NewFileDevStore("./.kitsune-snapshots")

runner := pipeline.Apply(urls).ForEach(handle)
summary, err := runner.Run(ctx, kitsune.WithDevStore(store))
```

The first run with an empty `./.kitsune-snapshots` directory captures everything. The second run with the same directory replays everything: no fetch, no enrich, no publish. Your downstream `ForEach` sees the same items it would have seen if the pipeline had run live.

### 3. Iterate on the segment you care about

To re-run a specific segment live (because you changed its logic, or you want to refresh its output), delete its snapshot file:

```bash
rm ./.kitsune-snapshots/enrich.json
```

The next run replays `fetch` from snapshot, runs `enrich` live, captures the new `enrich.json`, then replays or runs `publish` according to whether `publish.json` exists.

The whole iteration loop becomes: change code → delete one or more snapshot files → re-run.

---

## Unit-testing downstream stages with `FromCheckpoint`

When a `DevStore` has captured a segment's output, you can drive a downstream stage from that snapshot without re-running anything:

```go
store := kitsune.NewFileDevStore("testdata/checkpoints")

func TestPublishHandling(t *testing.T) {
    enriched := kitsune.FromCheckpoint[EnrichedPage](store, "enrich")

    var acks []string
    _, err := kitsune.Map(enriched, publish).
        ForEach(func(_ context.Context, ack string) error {
            acks = append(acks, ack)
            return nil
        }).Run(context.Background())

    if err != nil {
        t.Fatal(err)
    }
    // assert on acks
}
```

`FromCheckpoint[T]` is a regular pipeline source; you can compose it with `Map`, `Filter`, `Batch`, anything. It loads the snapshot at run time, unmarshals each item with `encoding/json`, and emits.

Failure modes:
- The snapshot is missing → `Run` returns an error wrapping `ErrSnapshotMissing`.
- The snapshot's items don't unmarshal as `T` → the source aborts on the first failed item with a wrapped error.

`FromCheckpoint` is independent of `WithDevStore`; you can use it without ever attaching a `DevStore` to a `Run`.

---

## Interaction with `Effect`

Replay bypasses inner stages **including** any `Effect` calls. This is usually what you want during dev iteration: you don't want each iteration to re-publish to SNS, re-write to the database, or re-call an external API.

But it has a sharp edge: a segment that reads as "publish to queue" silently turns into a no-op when its snapshot exists. If you genuinely want the effect to fire on every run, do one of:

1. Don't wrap effects in segments. An `Effect` outside any `Segment` always runs live.
2. Use `WithDevStore` only when iterating; production runs use no DevStore and effects always fire.
3. Delete the snapshot file before each run if you want a refresh.

For total wiring validation without effects, the [`DryRun()`](options.md#dryrun) RunOption is the inverse: it skips every `Effect` regardless of segment placement, so you can verify the graph topology without producing side effects.

---

## Interaction with `RunSummary`

A replayed segment does not register success or failure counts because no `Effect` ran inside it. So `RunSummary.Outcome` reflects only the segments that ran live during this run.

Concretely:
- Run 1 (live): all segments run; an `Effect` failure surfaces as `RunFailure` (or `RunPartialSuccess` for `BestEffort`).
- Run 2 (replayed): the failing segment is replayed from the captured-on-Run-1 snapshot; the `Effect` is skipped; `Outcome` is `RunSuccess` (assuming no other live failures).

This is consistent: the `RunSummary` describes *this* run, not the cumulative history of runs that produced the snapshots.

If you want to verify outcome derivation after a code change, delete all snapshots first to force a fully-live run.

---

## Custom `DevStore` implementations

`FileDevStore` is the built-in. Anything that satisfies the [`DevStore`](operators.md#devstore-interface) interface works:

```go
type DevStore interface {
    Save(ctx context.Context, segment string, items []json.RawMessage) error
    Load(ctx context.Context, segment string) ([]json.RawMessage, error)
}
```

Concrete examples of custom stores you might write:

- **Redis-backed `DevStore`**: share snapshots across a team. Each developer reads from the same captured outputs.
- **S3-backed `DevStore`**: long-lived snapshots for staging/QA.
- **In-memory `DevStore`**: useful in tests where you want capture/replay semantics without touching disk.

Stick to the contract: `Load` returns `ErrSnapshotMissing` (wrapped, so callers can use `errors.Is`) when no snapshot exists. `Save` accepts a slice of pre-marshaled `json.RawMessage` items; the items are owned by the caller and you should not retain them after `Save` returns.

A minimal in-memory store:

```go
type MemDevStore struct {
    mu   sync.Mutex
    data map[string][]json.RawMessage
}

func (m *MemDevStore) Save(_ context.Context, name string, items []json.RawMessage) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    if m.data == nil {
        m.data = map[string][]json.RawMessage{}
    }
    cp := make([]json.RawMessage, len(items))
    for i, item := range items {
        cp[i] = append(json.RawMessage(nil), item...)
    }
    m.data[name] = cp
    return nil
}

func (m *MemDevStore) Load(_ context.Context, name string) ([]json.RawMessage, error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    items, ok := m.data[name]
    if !ok {
        return nil, fmt.Errorf("%w: segment %q", kitsune.ErrSnapshotMissing, name)
    }
    return items, nil
}
```

---

## Interaction with fusion

Attaching a `Segment` disables stage fusion at its **output boundary**. The hot-path `Map → Filter → ForEach` chain still fuses inside a segment, but the segment's last stage cannot fuse with a downstream stage that lives outside the segment. This is necessary so the segment's build wrapper can intercept the output channel for `SegmentName` stamping and DevStore capture/replay.

In practice this is rarely a concern: segments are an authoring-time grouping mechanism, and the stages inside them still fuse normally. If you have a hot inner loop that depends on full chain fusion, keep that loop inside one segment rather than splitting it across two.

See the [tuning guide](tuning.md) for the full fusion-eligibility table.

---

## Safety, scope, and limits

`DevStore` is **strictly a development affordance**:

- **No schema versioning.** If a segment's output type changes (a renamed field, a new required field), the old snapshot will silently produce zero values or partial fields on replay. Delete the snapshot.
- **JSON-based serialization.** Round-trips through `json.Marshal` / `json.Unmarshal`. Types that lose fidelity in JSON (e.g. types with custom `MarshalJSON` that drops fields, channels, function values) cannot be captured faithfully.
- **No concurrency safety beyond what the implementation provides.** `FileDevStore` serialises writes with a mutex, but two processes pointing at the same directory will race. Do not share a snapshot directory across concurrent `Run` calls.
- **Capture is best-effort.** If `Save` fails (disk full, permission error), the failure is silently dropped: capture is dev-only and a save error should not fail the run. The next run will re-capture.
- **Capture buffers in memory.** A single segment's output is accumulated as `[]json.RawMessage` before `Save` is called. For very large segments this is a memory cost.
- **Empty-named segments are ignored.** Stages outside any `Segment`, and segments constructed via the zero `Segment[I, O]{}` value, are not captured or replayed.

Attaching a `DevStore` to a production run is an explicit choice. The recommended pattern is to gate it behind a build tag, environment variable, or explicit dev script:

```go
var store kitsune.DevStore
if os.Getenv("KITSUNE_DEVSTORE") != "" {
    store = kitsune.NewFileDevStore("./.kitsune-snapshots")
}

opts := []kitsune.RunOption{}
if store != nil {
    opts = append(opts, kitsune.WithDevStore(store))
}
runner.Run(ctx, opts...)
```

---

## What's not included

### `ReplayThrough` was deferred

The original design called for a `ReplayThrough(name)` RunOption that would "replay all segments up to and including `name`, then run subsequent segments live." That was deferred because "up to and including" and "subsequent" are ambiguous in branched DAGs (segments can run on parallel forks).

The pragmatic use case "iterate on segment X, replay everything upstream" is already covered by:

```bash
rm ./.kitsune-snapshots/X.json
# upstream snapshots remain; X re-runs live and recaptures
```

If a concrete use case demands a more declarative `ReplayThrough`, it can be added in a follow-up without breaking the current surface.

### No retention policy

Snapshots accumulate. `FileDevStore` does not garbage-collect or version snapshot files. If you rename a segment, the old file remains on disk. A periodic `rm -rf .kitsune-snapshots` is the simplest reset.

---

## Recipes

### Reset everything

```bash
rm -rf ./.kitsune-snapshots
```

### Reset only segments downstream of a refactor

```bash
rm ./.kitsune-snapshots/enrich.json ./.kitsune-snapshots/publish.json
# fetch.json remains; only enrich and publish re-run live
```

### Capture once, then unit-test downstream stages

```go
// One-off capture run:
runner.Run(ctx, kitsune.WithDevStore(kitsune.NewFileDevStore("testdata/checkpoints")))

// Subsequent unit test:
store := kitsune.NewFileDevStore("testdata/checkpoints")
src := kitsune.FromCheckpoint[EnrichedPage](store, "enrich")
// ... test the publish logic against the snapshot ...
```

### Validate wiring without side effects

```go
runner.Run(ctx, kitsune.DryRun()) // skips every Effect; pure stages run normally
```

`DryRun` and `WithDevStore` are independent: you can combine them, but they solve different problems.

---

## Where to next

- [Operator catalog → DevStore](operators.md#devstore) for the type and option signatures.
- [Operator catalog → Segment](operators.md#segment) for the segment surface.
- [examples/devstore](https://github.com/zenbaku/go-kitsune/tree/main/examples/devstore) for a runnable end-to-end demo.
- [examples/segment](https://github.com/zenbaku/go-kitsune/tree/main/examples/segment) for a segment-only demo.
