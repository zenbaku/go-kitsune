# Design: Cooperative Drain — Full Operator Rollout

**Date:** 2026-04-14
**Status:** Approved
**Roadmap item:** `Cooperative drain: full operator rollout` (Performance section)

---

## Context

The cooperative drain prototype (committed in `feat(drain):` series) converted six operators:
`mapSerial`, `mapSerialFastPath`, `ForEach`, `Take`, `TakeWhile`, `Drop`, `DropWhile`.

The infrastructure is in `pipeline.go`:
- `drainEntry` struct (`chan struct{}` + `atomic.Int32` refs)
- `runCtx.initDrainNotify(producerID int64, consumerCount int32)`
- `runCtx.signalDrain(producerID int64)`
- `runCtx.drainCh(id int64) <-chan struct{}`

Roughly 50+ `go internal.DrainChan(...)` calls remain across 12 files. This design covers their complete removal.

---

## New infrastructure

### `initMultiOutputDrainNotify` on `runCtx`

Fan-out operators (Partition, Broadcast, Balance, KeyedBalance, GroupByStream) produce multiple output channels from a single stage goroutine. All outputs must be drained before the fan-out stage exits.

```go
// initMultiOutputDrainNotify registers ONE shared drainEntry for a fan-out stage.
// All outputIDs map to the same entry with refs = totalConsumers (the sum of
// out[i].consumerCount.Load() across all output pipelines).
// When any consumer calls signalDrain(outputID), the shared counter decrements.
// When the counter reaches zero the drain channel closes, unblocking the stage's
// select loop via drainCh(outputIDs[0]).
func (rc *runCtx) initMultiOutputDrainNotify(outputIDs []int64, totalConsumers int32)
```

Implementation: create one `drainEntry`, store it in `rc.drainNotify` under every ID in `outputIDs`.

---

## Operator conversion groups

### Group 1 — Single-input / single-output (mechanical pattern)

Files: `batch.go`, `aggregate.go`, `misc.go`, `advanced.go`, `notification.go`, `compat.go`, `operator_filter.go` (slow path), `operator_flatmap.go`, `state_with.go`, `state_withkey.go`, `enrich.go`, `collect.go`, `operator_transform.go`

Pattern per operator:

```go
// In build():
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    // ...channel/meta setup...
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(ch)
        defer func() { rc.signalDrain(p.id) }()  // replaces go DrainChan(inCh)

        for {
            select {
            case item, ok := <-inCh:
                if !ok { return nil }
                select {
                case ch <- item:
                case <-ctx.Done(): return ctx.Err()
                case <-drainCh:    return nil
                }
            case <-ctx.Done(): return ctx.Err()
            case <-drainCh:    return nil
            }
        }
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
```

Operators that use `internal.NewBlockingOutbox` or `internal.NewOutbox` for sending still add `drainCh` to their outer receive select, but the outbox send is left as-is (outbox already respects `ctx.Done()`; drain exit is triggered at the outer receive loop level).

For operators with an inner `func() error` (like `mapSerial`), the `drainCh` arm is added to that inner select. The outer supervision wrapper returns when the inner returns.

### Group 2 — Multi-input fan-in

Operators: `Zip`, `ZipWith`, `CombineLatest`, `WithLatestFrom`, `Amb`, `Merge` (`fan_out.go`), `TakeUntil`, `SkipUntil` (`operator_take.go`)

Pattern difference from Group 1:
- Signal drain to **all** inputs on exit: `defer rc.signalDrain(a.id)` + `defer rc.signalDrain(b.id)`
- Own output still uses standard `initDrainNotify` + `drainCh` monitoring

For `Merge` (N inputs): `defer` a loop over all `pipelines[i].id` calling `signalDrain`.

For `TakeUntil`/`SkipUntil`: signal drain to both `p.id` and `boundary.id`. Add `drainCh` arm to the main select.

For operators that spin worker goroutines internally (e.g. `Merge` uses `sync.WaitGroup` + goroutines per input): the worker goroutines exit when their input channel closes or the outbox returns an error. The dispatch loop exits when `drainCh` fires. Worker goroutine exit is natural (they read from `inChan` which closes because upstream is now exiting cooperatively).

### Group 3 — Fan-out (single-input, multi-output)

Operators: `Partition`, `Broadcast`/`BroadcastN`, `Balance`, `KeyedBalance` (`fan_out.go`), `GroupByStream` (`advanced.go`)

Pattern:

```go
// In sharedBuild():
totalConsumers := int32(0)
for _, op := range outPipelines {
    totalConsumers += op.consumerCount.Load()
}
rc.initMultiOutputDrainNotify(ids, totalConsumers)
drainCh := rc.drainCh(ids[0])  // all ids share one entry

stage := func(ctx context.Context) error {
    defer func() {
        for _, c := range chans { close(c) }
    }()
    defer func() { rc.signalDrain(p.id) }()  // replaces go DrainChan(inCh)

    for {
        select {
        case item, ok := <-inCh:
            if !ok { return nil }
            // distribute to outputs...
            if err := ...; err != nil { return err }
        case <-ctx.Done(): return ctx.Err()
        case <-drainCh:    return nil
        }
    }
}
```

For `GroupByStream`: it creates sub-pipelines dynamically. Each sub-pipeline's consumer count is known at build time via `out.consumerCount`. The combined drain uses `initMultiOutputDrainNotify` with the IDs and total consumer counts of all statically-known output pipelines. (Dynamic sub-pipelines created at runtime are not tracked in drainNotify; they exit via context cancellation as before.)

### Group 4 — Sources

File: `source.go` (`sourceStage` helper and all source constructors)

Update `sourceStage` signature:

```go
func sourceStage[T any](ch chan T, gate *internal.Gate, drainCh <-chan struct{},
    itemFn func(ctx context.Context, send func(T) error) error) stageFunc
```

The `send` helper gains a drain arm:

```go
send := func(item T) error {
    select {
    case ch <- item:
        return nil
    case <-drainCh:
        return internal.ErrDrained  // new sentinel, causes itemFn to return
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

Each source's `build` function calls `rc.initDrainNotify(id, out.consumerCount.Load())` and passes `rc.drainCh(id)` to `sourceStage`.

Sources that do not use `sourceStage` (e.g. `Channel[T]`, `From`) are updated individually with the same pattern.

`internal.ErrDrained` is a new package-level sentinel error that `sourceStage` returns from `itemFn` when the drain fires. Callers of the stage (the runner) see a nil-equivalent signal; this is handled by filtering it in the run loop (or by treating it as nil in the stage error check).

Actually, simpler: the send helper returns `ctx.Err()` via a modified context, or returns a well-known error that the `sourceStage` wrapper converts to `nil` before returning. The cleanest approach: `sourceStage` returns `nil` when `send` returns `ErrDrained`.

### Group 5 — Map concurrent paths + fused fast path

**`mapConcurrent` and `mapOrdered`** (`operator_map.go`):

Both use a dispatcher goroutine that reads from `inCh` and distributes to per-worker channels. Add `drainCh` to the dispatcher's outer select loop. Workers exit naturally when the dispatcher closes their channels or the outbox returns an error. After the dispatcher exits, call `signalDrain(p.id)`.

The `rc.initDrainNotify` and `drainFn`/`drainCh` setup is already done in `Map`'s build function for the serial paths; extend it to the `concurrency > 1` branches.

**Fused fast path** (`fusionEntry` in `operator_map.go`):

The fused chain runs inside ForEach as a single goroutine. ForEach already has cooperative drain and signals drain to its input chain when it exits. The fusionEntry's `inCh` is the non-fused upstream's channel. When ForEach exits (via its own `drainCh`), it closes the fused stage goroutine by returning from the stage function, which naturally causes the fusionEntry goroutine to exit when it tries to read from `inCh` (closed by upstream). The remaining `go internal.DrainChan(inCh)` in `fusionEntry` is only reached when the fused goroutine itself exits early — replace with `signalDrain(p0.id)`. Add `initDrainNotify` for the fusionEntry's upstream (`p0.id`).

---

## Correctness considerations

- `initDrainNotify` must be called exactly once per stage, during `build()`, before the stage goroutine starts
- `signalDrain` is idempotent for the "no entry" case (no-op); safe to call in all defers
- Fan-out operators capture `out` pipelines before the `sharedBuild` closure runs; `consumerCount.Load()` is correct because all downstream `track()` calls happen at construction time, before `Run`
- For `initMultiOutputDrainNotify` with `totalConsumers == 0`: clamp to 1 (same semantics as `initDrainNotify`)
- The `ErrDrained` sentinel used in sources must NOT propagate as a real error to the runner. Either: (a) `sourceStage` converts it to `nil` before returning, or (b) filter in the runner's error collection loop. Option (a) is cleaner.

---

## Testing

- All existing tests pass with `task test:race`
- Extend `drain_test.go` goroutine-count test to cover:
  - Multi-input: `Merge([src1, src2]) → Take(1)` — 0 drain goroutines
  - Fan-out: `Broadcast(src, 2) → [Take(1), Take(1)]` — 0 drain goroutines
  - Source: `FromSlice → Take(1)` — 0 drain goroutines (currently spawns 1)
- `task test:property` continues to validate operator algebra invariants
- Benchmark: `BenchmarkDrainBurst` in `drain_bench_test.go` should show further improvement (fewer goroutines, lower allocs/op for Retry-heavy pipelines)

---

## Files changed

| File | Change |
|---|---|
| `pipeline.go` | Add `initMultiOutputDrainNotify`; add `ErrDrained` sentinel |
| `source.go` | Update `sourceStage` signature; add `initDrainNotify` to all sources |
| `batch.go` | Group 1 conversion (Batch, ChunkBy, ChunkWhile, SlidingWindow, SessionWindow, BufferWith) |
| `aggregate.go` | Group 1 conversion (Scan, Reduce, Aggregate variants) |
| `misc.go` | Group 1 conversion (Distinct, Debounce, Throttle, etc.) |
| `advanced.go` | Group 1 + Group 3 (ExpandMap Group 1; GroupByStream Group 3) |
| `notification.go` | Group 1 conversion |
| `compat.go` | Group 1 conversion |
| `operator_filter.go` | Group 1 conversion (slow path; filterFastPath) |
| `operator_flatmap.go` | Group 1 conversion (FlatMap, ConcatMap) |
| `state_with.go` | Group 1 conversion (MapWith, FlatMapWith) |
| `state_withkey.go` | Group 1 conversion (MapWithKey, FlatMapWithKey) |
| `enrich.go` | Group 1 conversion (LookupBy, Enrich) |
| `collect.go` | Group 2 conversion (Collect multi-input) |
| `fan_combine.go` | Group 2 conversion (Zip, CombineLatest, WithLatestFrom, Amb) |
| `fan_out.go` | Group 2 (Merge) + Group 3 (Partition, Broadcast, Balance, KeyedBalance) |
| `operator_take.go` | Group 2 conversion (TakeUntil, SkipUntil) |
| `operator_map.go` | Group 5 (mapConcurrent, mapOrdered, fusionEntry) |
| `drain_test.go` | Extended goroutine-count assertions |
