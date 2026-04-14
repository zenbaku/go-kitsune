# Design: Cooperative Drain for DrainChan Goroutine Burst

**Date:** 2026-04-14
**Status:** Approved
**Roadmap item:** `DrainChan goroutine burst on mass teardown` (Performance section)

---

## Problem

Every transform stage contains:

```go
defer func() { go internal.DrainChan(inCh) }()
```

When a downstream stage exits early (e.g. `Take(1)`, `TakeWhile`), it stops reading from its input channel but the upstream stage may be blocked sending into a full buffer. The `DrainChan` goroutine unblocks the upstream by consuming from the channel.

On a 20-stage pipeline with `Take(1)`, teardown spawns 20 simultaneous drain goroutines. For pipelines that cycle frequently (via `Retry`), this creates sustained goroutine pressure: exactly the scenario described in the roadmap.

### Why `ctx.Done()` does not help

The errgroup context is only cancelled when a stage returns a **non-nil** error. `Take(1)` and `TakeWhile` return `nil` on success. Upstream stages cannot exit via `ctx.Done()` because the context is never cancelled. Without `DrainChan`, Map would be blocked on a full channel send indefinitely.

---

## Chosen approach: Cooperative drain via drainNotify channels

Each stage gets a drain-notification channel. When a consumer exits, instead of spawning a goroutine to drain the producer's output, it closes a signal channel that the producer monitors in its existing select loop. The producer exits cleanly, which cascades upstream without any additional goroutines.

### Alternatives considered

**B: Single drain coordinator goroutine:** Replace N goroutines with 1 coordinator. Simpler, but still spawns goroutines and doesn't solve the fundamental blocking problem.

**C: Sentinel error + filtered context cancellation:** Make `signalDone` also cancel the errgroup context via a filtered sentinel. Zero goroutines, but widens cancellation to all stages and interacts poorly with the fused fast path.

---

## Data structures

Two new fields on `runCtx`:

```go
type drainEntry struct {
    ch   chan struct{}
    refs atomic.Int32
}

// Added to runCtx:
drainNotify map[int64]*drainEntry  // producerID → entry
```

Three new methods on `runCtx`:

```go
// initDrainNotify registers a drain-notify entry for producerID.
// consumerCount is the number of downstream stages that will consume
// the producer's output channel. Call once per stage during build().
func (rc *runCtx) initDrainNotify(producerID int64, consumerCount int32)

// signalDrain decrements the producer's ref count. When it reaches zero,
// the drainNotify channel is closed, unblocking the producer's select.
// Call in each consumer stage's defer, replacing go DrainChan(inCh).
func (rc *runCtx) signalDrain(producerID int64)

// drainCh returns the drainNotify channel for id. Stages add this to
// their select loops so they exit when all consumers are gone.
func (rc *runCtx) drainCh(id int64) <-chan struct{}
```

`consumerCount` is read from `out.consumerCount.Load()` where `out` is the `*Pipeline[O]` returned by the operator constructor. By the time `build()` is called (during `Run`), all downstream `track()` calls have already run and `consumerCount` is correct. Each operator's `build` closure captures `out` via a `var out *Pipeline[O]` pointer declared before the closure.

---

## Operator contract

### In `build()`: initialize and capture:

```go
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    if existing := rc.getChan(id); existing != nil {
        return existing.(chan O)
    }
    inCh := p.build(rc)
    // ... channel/meta setup ...
    consumers := out.consumerCount.Load()
    rc.initDrainNotify(id, consumers)
    drainCh := rc.drainCh(id)
    // ... register stage ...
}
out = newPipeline(id, meta, build)
return out
```

### In the stage function: replace DrainChan, add drainCh to selects:

```go
stage := func(ctx context.Context) error {
    defer close(ch)
    defer func() { rc.signalDrain(p.id) }()  // was: go internal.DrainChan(inCh)

    for {
        select {
        case item, ok := <-inCh:
            if !ok { return nil }
            select {
            case ch <- item:
            case <-ctx.Done():  return ctx.Err()
            case <-drainCh:    return nil   // all consumers gone
            }
        case <-ctx.Done():  return ctx.Err()
        case <-drainCh:    return nil        // all consumers gone
        }
    }
}
```

### Terminal stages (ForEach, Collect, etc.):

Terminals signal drain to their upstream but do not produce an output channel, so no drainNotify of their own:

```go
defer func() { rc.signalDrain(p.id) }()  // was: go internal.DrainChan(inCh)
// no drainCh select needed: terminals read to completion
```

---

## Prototype scope

The prototype converts the following operators. This is sufficient to validate the approach on the common linear pipeline case:

| Operator | File | Status |
|---|---|---|
| `Map` (single-consumer path) | `operator_map.go` | converted |
| `Filter` | `operator_filter.go` | converted |
| `Take` | `operator_take.go` | converted |
| `TakeWhile` | `operator_take.go` | converted |
| `Drop` | `operator_take.go` | converted |
| `DropWhile` | `operator_take.go` | converted |
| `ForEach` (terminal) | `terminal.go` | converted |

### Operators NOT converted in this prototype (follow-on work)

The following operators still use `go internal.DrainChan(inCh)` after this prototype. Full conversion is tracked in `doc/roadmap.md`:

| Operator / file | Reason deferred |
|---|---|
| `Map` concurrent variants (`Concurrency > 1`) | More complex select topology |
| Fused fast path (`operator_map.go` fusionEntry) | Different execution model; no outer select |
| `Batch`, `BufferWith`, `ChunkBy`, `ChunkWhile`, `SlidingWindow`, `SessionWindow` | `batch.go`: stateful, needs careful drain ordering |
| `FlatMap`, `ConcatMap` | `operator_flatmap.go`: inner pipeline lifecycle |
| `Merge`, `Amb`, `Zip`, `CombineLatest`, `WithLatestFrom` | Multi-input: drain ref count must cover multiple producers |
| `TakeUntil`, `SkipUntil` | Two input channels, drain both producers |
| `GroupByStream`, `Balance`, `Partition`, `Broadcast` | Fan-out/fan-in: complex consumer topology |
| `MapWith`, `MapWithKey`, `FlatMapWith`, `FlatMapWithKey` | `state_with.go`, `state_withkey.go`: internal goroutines |
| `Scan`, `Reduce`, `Aggregate` | `aggregate.go` |
| `LookupBy`, `Enrich` | `enrich.go` |
| All sources (`Ticker`, `Repeatedly`, etc.) | Sources should also monitor `drainCh` for their output; deferred |

---

## Correctness verification

1. `task test:race`: existing suite with race detector; no new races.
2. All existing `Take` / `TakeWhile` / `ForEach` tests pass unchanged.
3. New test: goroutine count assertion on a 20-stage `Map → Take(1)` pipeline. `runtime.NumGoroutine()` snapshotted immediately after `Run` returns minus a pre-run baseline should equal 0 (or within 1 for the runner itself). With the old path, this would be ~20.

---

## Benchmark plan

New file `drain_bench_test.go`:

```
BenchmarkDrainBurst/old  : 20-stage pipeline, Take(1), Retry loop
BenchmarkDrainBurst/new  : same pipeline, cooperative drain operators
```

Key metrics: peak goroutine count during teardown, ns/op for the Retry cycle, allocs/op. A measurable improvement on any metric is the signal to proceed with full library rollout.

---

## Roadmap follow-on

A new roadmap item will be added to `doc/roadmap.md` tracking full conversion of all operators listed in the "not converted" table above.
