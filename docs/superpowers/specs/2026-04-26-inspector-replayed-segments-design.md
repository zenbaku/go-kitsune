# Inspector visibility for replayed Segments — design

**Status:** ready for implementation
**Date:** 2026-04-26
**Roadmap item:** Long-term → "Inspector visibility for replayed Segments"

---

## Problem

When a `Run` is started with `WithDevStore` and a snapshot exists for a named `Segment`, `tryReplaySegment` (in `segment.go`) bypasses the inner stage chain and spawns an out-of-band goroutine that emits cached items directly into a fresh output channel. As a consequence:

1. None of the inner segment stages call `rc.add`, so they never appear in `rc.stages` or `rc.metas`.
2. `OnGraph` therefore omits them entirely. The hull on the inspector dashboard renders empty.
3. The replay goroutine never calls `OnStageStart` / `OnItem` / `OnStageDone`, so `MetricsHook` and any user `Hook` see no activity for the replayed segment.

The user-visible result is that a replayed segment looks like *nothing happened where the segment used to be*. There is no way to tell from the dashboard whether the segment was replayed (cache hit), captured (cache miss, ran live), or absent.

## Goal

Make replayed segments first-class observable units. The inspector dashboard, `MetricsHook`, and any user-supplied `Hook` should all see the replay as a real stage — distinguishable from a live-executed segment but otherwise indistinguishable from any other stage in the way it is observed.

## Non-goals

- No persistence-format changes to `InspectorStore`.
- No new optional `Hook` interfaces.
- No changes to `captureSegment`. Capture-mode runs are already observable through the inner stages' normal hook events.
- No retroactive support for runs that started before this feature ships.

## Approach

Convert `tryReplaySegment` from an out-of-band goroutine into a proper synthetic stage in the run's stage graph.

### Architecture

Inside the segment build wrapper, when a snapshot is found, the wrapper now:

1. Allocates the output channel `out := make(chan T, 16)` (same as today).
2. Constructs a `stageMeta` with:
   - `name = <segment-name>`
   - `kind = "segment-replay"`
   - `segmentName = <segment-name>`
   - `inputs = nil` (replay is source-like)
   - `getChanLen` / `getChanCap` closures over `out`
3. Constructs a `stageFunc` that runs the replay loop.
4. Calls `rc.add(fn, meta)` so the stage participates in `rc.stages`/`rc.metas`.
5. Returns `out` as the segment's output channel.

The replay `stageFunc` does:

```
hook.OnStageStart(ctx, name)
defer close(out)

processed, errs := 0, 0
for _, data := range raws {
    select {
    case <-ctx.Done():
        hook.OnStageDone(ctx, name, processed, errs)
        return ctx.Err()
    default:
    }

    var item T
    if err := json.Unmarshal(data, &item); err != nil {
        errs++
        hook.OnItem(ctx, name, 0, err)
        break  // stop on first decode failure (matches existing semantics)
    }

    select {
    case <-ctx.Done():
        hook.OnStageDone(ctx, name, processed, errs)
        return ctx.Err()
    case out <- item:
        processed++
        hook.OnItem(ctx, name, 0, nil)
    }
}
hook.OnStageDone(ctx, name, processed, errs)
return nil
```

`captureSegment` is unchanged.

### Hook contract

- **Stage name** observed by hooks: exactly the segment name. No prefix or suffix.
- **`OnItem` duration**: `0` for replayed items. The dashboard renders avg-latency as `—` for `kind == "segment-replay"` rows so the UI does not display a misleading "0ns".
- **`OnItem` error**: `nil` on successful sends; non-nil only on a per-item `json.Unmarshal` failure.
- **Decode-failure semantics**: stop emission on first failure, fire `OnItem` once with the decode error, then `OnStageDone` with partial counts. Match the existing abort-on-failure behavior; just make it visible.
- **Behavior change for user hooks**: a user-supplied `Hook` will now see synthetic `OnStageStart` / `OnItem` / `OnStageDone` calls under the segment name on replay runs. This is observable, intentional, and only happens when `WithDevStore` is attached and a snapshot is present.

### GraphNode wire format

- `Kind` carries the new value `"segment-replay"`. The field is already `string`; no struct shape change.
- `SegmentName` is set to the segment name (so dashboard hull-grouping renders the synthetic node inside the hull).
- `Inputs` is empty.
- No new fields. No persistence-shape changes to `InspectorStore`.

### Inspector UI

Two additions to `inspector/ui.html`:

1. **REPLAY badge**: in the SVG node renderer that already attaches `REQ` / `BE` corner badges for effect nodes, add a third case for `kind === 'segment-replay'`. Use a distinct color (proposed: amber, separate from green/red used for effect required/best-effort).
2. **Avg-latency cell**: in the stage-table row renderer, when the row's stage's graph node has `kind === 'segment-replay'`, render the avg-latency cell as `—`. Items, rate, and errors columns render normally.

The hull label and tinted dashed background already render today from `segment_name` grouping; no hull-rendering changes are needed.

### Edge cases

| Case | Behavior |
|---|---|
| Empty snapshot (`raws` nil or empty) | `OnStageStart`, no items, `OnStageDone(name, 0, 0)`, channel closes. Hull shows `0 items`. |
| Decode failure mid-stream | `OnItem(name, 0, decodeErr)` for the failing item, stop emission, `OnStageDone` with partial counts. Downstream receives the items that made it through before the failure. |
| Context cancellation mid-replay | `select`-per-item exits cleanly. `OnStageDone` still fires; the goroutine returns `ctx.Err()`. |
| Empty segment name | Existing guard (`rc.devStore != nil && name != ""`) keeps this path unreachable. |
| No DevStore | Build wrapper falls through to `originalBuild(rc)`. No synthetic stage. |
| `OnBuffers` query during replay | The new stage's `getChanLen`/`getChanCap` closures point at `out`. Buffer occupancy is reported for free. |

### Compatibility

- Pre-existing user `Hook` implementations: see new events under the segment name on replay runs only. Considered desirable.
- Pre-existing `MetricsHook` users: `Stages["<segment-name>"]` populates with `Processed` (replayed item count), `Errors` (decode failures), latency histogram (zeros). `Snapshot()` reflects this. Considered desirable.
- Inspector clients on an older UI build: `kind="segment-replay"` is unknown; the badge does not render and avg-latency renders as `0` instead of `—`. Behavior is degraded but not broken. Inspector and DevStore are dev-only; tolerable.

## Testing

Four tests:

1. **`TestInspector_SegmentReplay_HappyPath`** (`inspector/inspector_test.go`)
   Pre-seed a `FileDevStore` directory with a snapshot for segment `"my-seg"`. Build a pipeline with that segment, attach `inspector.New()`, run with `WithDevStore`. Assert: `/state` includes a stage `"my-seg"` with `Items == N`, `Status == "done"`. The graph payload includes a node with `Kind == "segment-replay"` and `SegmentName == "my-seg"`.

2. **`TestInspector_SegmentReplay_DecodeFailure`** (same file)
   Snapshot contains one valid item then one corrupt JSON blob. Run. Assert: `Items == 1`, `Errors == 1`, `Status == "done"`. Downstream consumer received exactly the one valid item.

3. **`TestInspector_SegmentReplay_CaptureThenReplay`** (same file)
   Run 1 with no snapshot — capture. Assert inner stages fire normally and the snapshot file exists on disk after the run. Run 2 with the same DevStore — replay. Assert the synthetic stage appears in the graph in place of the inner stages, and item counts on the synthetic stage match Run 1's downstream item count.

4. **`TestSegment_ReplayContextCancel`** (`segment_test.go`)
   Snapshot with many items (large enough that cancel can land mid-replay). Cancel the run context shortly after start. Assert: the goroutine returns, the output channel is closed, and `OnStageDone` was called.

Run gates:
- `task test` (must pass).
- `task test:race` (must pass; the new `select`-per-item path under hook calls is the riskiest concurrency change).

## File touch list

| File | Change |
|---|---|
| `segment.go` | Rewrite `tryReplaySegment` to register a synthetic stage via `rc.add` with the replay loop as `stageFunc`; respect `ctx.Done()` per item; surface decode failures via `OnItem`. |
| `inspector/ui.html` | Add `REPLAY` badge case for `kind="segment-replay"`; render avg-latency as `—` for those rows. |
| `inspector/inspector_test.go` | Tests 1–3 (`SegmentReplay_HappyPath`, `_DecodeFailure`, `_CaptureThenReplay`). |
| `segment_test.go` | Test 4 (`ReplayContextCancel`). |
| `doc/inspector.md` | Document the REPLAY badge and `segment-replay` stage kind in the dashboard guide. |
| `doc/dev-iteration.md` | Cross-link: "When the inspector is attached, replayed segments render as a REPLAY stage in the dashboard hull." |
| `doc/roadmap.md` | Mark the item `[x]` and move the description to past-tense done text. |
| Memory `project_higher_level_authoring.md` | Note that replayed segments are now first-class observable stages. |

## Spec self-review

- **Placeholders**: none. Every section has concrete content.
- **Internal consistency**: hook-contract, GraphNode-shape, UI rendering, and tests all reference the same `kind="segment-replay"` value and the same per-item duration/error semantics.
- **Scope**: a single implementation plan. The synthetic-stage refactor of `tryReplaySegment`, the UI badge, and the four tests are tightly coupled and ship together.
- **Ambiguity**: `getChanLen`/`getChanCap` closures: must reference `out` (the send-side channel) the same way other source stages do; code-level reference is `cap(out)` and `len(out)` evaluated lazily.
