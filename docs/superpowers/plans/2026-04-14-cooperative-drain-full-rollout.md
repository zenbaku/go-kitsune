# Cooperative Drain Full Operator Rollout — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every `go internal.DrainChan(inCh)` call in the library with cooperative drain signals, eliminating goroutine bursts on pipeline teardown.

**Architecture:** Each operator's `build()` function calls `rc.initDrainNotify(id, out.consumerCount.Load())`, captures `drainCh := rc.drainCh(id)`, and adds `case <-drainCh: return nil` to its select loops. On exit, `defer func() { rc.signalDrain(p.id) }()` replaces the drain goroutine. Fan-out operators (multiple output channels from one stage) use a new `initMultiOutputDrainNotify` helper that maps all output IDs to a single shared drain entry.

**Tech Stack:** Go 1.22+, standard library only. `task test:race` after each task.

---

## File map

| File | Change |
|---|---|
| `pipeline.go` | Add `initMultiOutputDrainNotify`; add `errDrained` sentinel |
| `source.go` | Update `sourceStage` to accept + monitor `drainCh`; update all call sites |
| `operator_filter.go` | Group 1 slow paths; Group 5 fusionEntry fallback |
| `operator_flatmap.go` | Group 1: all flatMap variants (serial fast-path, serial, concurrent, ordered) |
| `batch.go` | Group 1: Batch, Unbatch, Window, SlidingWindow, SessionWindow, ChunkBy, ChunkWhile; Group 2: BufferWith |
| `aggregate.go` | Group 1: Scan, Reduce, GroupByStream, FrequenciesByStream, and all aggregate operators |
| `misc.go` | Group 1: DefaultIfEmpty, Timestamp, TimeInterval, Sort, SortBy; Group 3: Unzip |
| `middleware.go` | Group 1: RateLimit |
| `notification.go` | Group 1: Dematerialize |
| `compat.go` | Group 1: WindowByTime |
| `operator_transform.go` | Group 1: WithIndex, Pairwise, TakeEvery, DropEvery, MapEvery, Intersperse |
| `state_with.go` | Group 1: mapWithSerial/Concurrent/Ordered, flatMapWithSerial/Concurrent/Ordered |
| `state_withkey.go` | Group 1: mapWithKeySerial/Concurrent/Ordered, flatMapWithKeySerial/Concurrent/Ordered |
| `advanced.go` | Group 1: SwitchMap, ExhaustMap, MapRecover; Group 3: MapResult |
| `fan_combine.go` | Group 2: ZipWith, CombineLatestWith, WithLatestFromWith, SampleWith |
| `operator_take.go` | Group 2: TakeUntil, SkipUntil |
| `fan_out.go` | Group 2: Merge; Group 3: Partition, Broadcast/BroadcastN, Balance, KeyedBalance, Share |
| `collect.go` | Group 2: SequenceEqual |
| `operator_map.go` | Group 5: mapConcurrent, mapOrdered, fusionEntry |
| `drain_test.go` | Extended goroutine-count assertions |
| `doc/roadmap.md` | Mark item complete |

---

## Patterns reference

### Group 1 — Single-input / single-output

Every Group 1 operator needs these four changes. Study the converted `Scan` example below before converting other operators.

**1. Add `var out` forward declaration before `build`:**
```go
// BEFORE
build := func(rc *runCtx) chan S { ... }
return newPipeline(id, meta, build)

// AFTER
var out *Pipeline[S]
build := func(rc *runCtx) chan S { ... }
out = newPipeline(id, meta, build)
return out
```

**2. In build(), after `rc.setChan(id, ch)`, add:**
```go
rc.initDrainNotify(id, out.consumerCount.Load())
drainCh := rc.drainCh(id)
```

**3. Replace drain goroutine with signalDrain (inside the stage func):**
```go
// BEFORE
defer func() { go internal.DrainChan(inCh) }()

// AFTER
cooperativeDrain := false
defer func() {
    if !cooperativeDrain {
        go internal.DrainChan(inCh)
    }
}()
defer func() { rc.signalDrain(p.id) }()
```

**4. Add `drainCh` arm to every select in the stage func:**
```go
case <-ctx.Done():
    return ctx.Err()
case <-drainCh:
    cooperativeDrain = true
    return nil
```

For operators that use an inner `func() error` (e.g. mapSerial calls `Supervise`), add `drainCh` to the inner function's select — the outer Supervise wrapper exits when the inner returns.

For operators using `internal.NewBlockingOutbox` for sends: `drainCh` goes in the outer receive select only (not the outbox.Send call). The outbox Send is unaffected.

### Group 2 — Multi-input fan-in

Same as Group 1 but signal drain to **all** inputs on exit:
```go
defer func() { rc.signalDrain(a.id) }()
defer func() { rc.signalDrain(b.id) }()
// (one per input pipeline)
```

The `cooperativeDrain` flag is set when either input's drain fires. All input channels still need `go internal.DrainChan` fallback for the non-cooperative path. (Each input gets its own `cooperativeDrainX` bool, or use a single shared bool since any early exit makes the whole stage exit.)

### Group 3 — Fan-out (multi-output from one stage)

Uses `initMultiOutputDrainNotify`. Pattern for a 2-output operator:
```go
var outA *Pipeline[A]
var outB *Pipeline[B]

sharedBuild := func(rc *runCtx) (chan A, chan B) {
    if existing := rc.getChan(aID); existing != nil {
        return existing.(chan A), rc.getChan(bID).(chan B)
    }
    // ... channel/meta setup ...
    totalConsumers := outA.consumerCount.Load() + outB.consumerCount.Load()
    rc.initMultiOutputDrainNotify([]int64{aID, bID}, totalConsumers)
    drainCh := rc.drainCh(aID) // any of the output IDs — they share one entry

    stage := func(ctx context.Context) error {
        defer close(aCh)
        defer close(bCh)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        for {
            select {
            case item, ok := <-inCh:
                if !ok { return nil }
                // distribute...
            case <-ctx.Done(): return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return aCh, bCh
}

outA = newPipeline(aID, aMeta, func(rc *runCtx) chan A {
    a, _ := sharedBuild(rc)
    return a
})
outB = newPipeline(bID, bMeta, func(rc *runCtx) chan B {
    _, b := sharedBuild(rc)
    return b
})
return outA, outB
```

---

## Task 1: Add `initMultiOutputDrainNotify` and `errDrained` to `pipeline.go`

**Files:**
- Modify: `pipeline.go`

- [ ] **Step 1: Add the helper after the existing `initDrainNotify` method (~line 198 in pipeline.go)**

```go
// initMultiOutputDrainNotify registers ONE shared drainEntry for a fan-out stage.
// All outputIDs map to the same entry with refs = totalConsumers (the sum of
// out[i].consumerCount.Load() across all output pipelines).
// When any consumer calls signalDrain(outputID), the shared counter decrements.
// When it reaches zero the drain channel closes, unblocking the stage's select.
func (rc *runCtx) initMultiOutputDrainNotify(outputIDs []int64, totalConsumers int32) {
	e := &drainEntry{ch: make(chan struct{})}
	n := totalConsumers
	if n < 1 {
		n = 1
	}
	e.refs.Store(n)
	for _, id := range outputIDs {
		rc.drainNotify[id] = e
	}
}
```

- [ ] **Step 2: Add the `errDrained` sentinel after the `drainEntry` type (around line 96)**

```go
// errDrained is returned by a source's send helper when the cooperative drain
// fires. sourceStage converts this to nil so it does not propagate as an error.
var errDrained = errors.New("kitsune: cooperative drain")
```

Add `"errors"` to the imports if not already present.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add pipeline.go
git commit -m "feat(drain): add initMultiOutputDrainNotify helper and errDrained sentinel"
```

---

## Task 2: Update `source.go` — cooperative drain for all sources

**Files:**
- Modify: `source.go`

The `sourceStage` helper is used by all sources. Update its signature and `send` helper to check `drainCh`. Sources that do not use `sourceStage` (e.g. `Channel[T]`, `From`) need direct updates.

- [ ] **Step 1: Update `sourceStage` signature and `send` helper**

```go
// BEFORE
func sourceStage[T any](ch chan T, gate *internal.Gate, itemFn func(ctx context.Context, send func(T) error) error) stageFunc {
	return func(ctx context.Context) error {
		defer close(ch)
		send := func(item T) error {
			if gate != nil {
				if err := gate.Wait(ctx); err != nil {
					return err
				}
			}
			select {
			case ch <- item:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return itemFn(ctx, send)
	}
}

// AFTER
func sourceStage[T any](ch chan T, gate *internal.Gate, drainCh <-chan struct{}, itemFn func(ctx context.Context, send func(T) error) error) stageFunc {
	return func(ctx context.Context) error {
		defer close(ch)
		send := func(item T) error {
			if gate != nil {
				if err := gate.Wait(ctx); err != nil {
					return err
				}
			}
			select {
			case ch <- item:
				return nil
			case <-drainCh:
				return errDrained
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := itemFn(ctx, send)
		if err == errDrained {
			return nil
		}
		return err
	}
}
```

- [ ] **Step 2: Find every call to `sourceStage` in `source.go` and add `rc.drainCh(id)` as the third argument**

Also add `rc.initDrainNotify(id, out.consumerCount.Load())` before `sourceStage` is called in each `build` closure. Each source that uses `sourceStage` needs the `var out *Pipeline[T]` forward declaration pattern.

For `FromSlice` (which has a fast-path that does NOT use `sourceStage`), update the fast-path stage directly:

```go
// fast path in FromSlice (the noop-hook, no-gate path):
var out *Pipeline[T]
build := func(rc *runCtx) chan T {
    // ... existing channel/meta setup ...
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)
    gate := rc.gate
    var stage stageFunc
    if internal.IsNoopHook(rc.hook) && gate == nil {
        stage = func(ctx context.Context) error {
            defer close(ch)
            for _, item := range items {
                select {
                case ch <- item:
                case <-drainCh:
                    return nil
                case <-ctx.Done():
                    return ctx.Err()
                }
                if ctx.Err() != nil {
                    return ctx.Err()
                }
            }
            return nil
        }
    } else {
        stage = sourceStage(ch, gate, drainCh, func(ctx context.Context, send func(T) error) error {
            for _, item := range items {
                if err := send(item); err != nil {
                    return err
                }
            }
            return nil
        })
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

Apply the same `var out` + `initDrainNotify` + `drainCh` pattern to every other source in `source.go`: `Channel[T]`, `From`, `Generate`, `Repeatedly`, `Ticker`, `Timer`, `Unfold`, `Iterate`, `Concat`, `Never`, `Empty`, and any others present.

For sources that have their own select loop (not using sourceStage), add `case <-drainCh: return nil` to every send-side select.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add source.go
git commit -m "feat(drain): convert all sources to cooperative drain"
```

---

## Task 3: Convert `operator_filter.go` — Group 1 slow paths

**Files:**
- Modify: `operator_filter.go`

`Filter` has 4 DrainChan calls: two slow-path stage funcs (standard and high-concurrency path), and two fusionEntry fallbacks. Convert the slow-path stages here. The fusionEntries are handled in Task 17.

- [ ] **Step 1: In `Filter`'s `build()`, add forward declaration and drainNotify after `rc.setChan`**

```go
// Add BEFORE the existing build closure:
var result *Pipeline[T]

// Inside build(), after rc.setChan(id, ch):
rc.initDrainNotify(id, result.consumerCount.Load())
drainCh := rc.drainCh(id)
```

Change the return at the bottom:
```go
// BEFORE
return newPipeline(id, meta, build)

// AFTER
result = newPipeline(id, meta, build)
// (fusionEntry setup follows as before)
return result
```

- [ ] **Step 2: Convert the slow-path stage func inside `Filter`**

```go
// BEFORE (inside the else branch of isFastPathEligible):
stage = func(ctx context.Context) error {
    defer close(ch)
    defer func() { go internal.DrainChan(inCh) }()
    // ...
    inner := func() error {
        for {
            select {
            case item, ok := <-inCh:
                // ...
            case <-ctx.Done():
                return ctx.Err()
            }
        }
    }
    return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
}

// AFTER:
stage = func(ctx context.Context) error {
    defer close(ch)
    cooperativeDrain := false
    defer func() {
        if !cooperativeDrain {
            go internal.DrainChan(inCh)
        }
    }()
    defer func() { rc.signalDrain(p.id) }()
    // ...
    inner := func() error {
        for {
            select {
            case item, ok := <-inCh:
                // ...
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
}
```

Repeat for the second slow-path stage func in the same file (the concurrency > 1 path, if present).

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add operator_filter.go
git commit -m "feat(drain): convert Filter slow paths to cooperative drain"
```

---

## Task 4: Convert `operator_flatmap.go` — Group 1 (all variants)

**Files:**
- Modify: `operator_flatmap.go`

FlatMap has 4 variants: `flatMapSerialFastPath`, `flatMapSerial`, `flatMapConcurrent`, `flatMapOrdered`. These are helper functions called from `FlatMap`'s `build`. They receive `inCh` and `outCh` directly.

To pass `drainCh` and `drainFn` in, update each helper's signature and the caller in `FlatMap`'s build.

- [ ] **Step 1: Add `var out` and `drainNotify` setup to `FlatMap`'s `build()` function**

```go
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    if existing := rc.getChan(id); existing != nil {
        return existing.(chan O)
    }
    inCh := p.build(rc)
    buf := rc.effectiveBufSize(cfg)
    ch := make(chan O, buf)
    m := meta
    m.buffer = buf
    m.getChanLen = func() int { return len(ch) }
    m.getChanCap = func() int { return cap(ch) }
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)
    drainFn := func() { rc.signalDrain(p.id) }
    // ... (existing hook, cfg setup) ...
    switch {
    case isFastPathEligible(cfg, hook) && cfg.concurrency == 1:
        stage = flatMapSerialFastPath(inCh, ch, fn, cfg.name, drainFn, drainCh)
    case cfg.concurrency > 1 && cfg.ordered:
        stage = flatMapOrdered(inCh, ch, fn, cfg, hook, drainFn, drainCh)
    case cfg.concurrency > 1:
        stage = flatMapConcurrent(inCh, ch, fn, cfg, hook, drainFn, drainCh)
    default:
        stage = flatMapSerial(inCh, ch, fn, cfg, hook, drainFn, drainCh)
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Update `flatMapSerialFastPath` signature and body**

```go
// BEFORE
func flatMapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, name string) stageFunc {
    return func(ctx context.Context) error {
        defer close(outCh)
        defer func() { go internal.DrainChan(inCh) }()
        // ...
    }
}

// AFTER
func flatMapSerialFastPath[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I, func(O) error) error, name string, drainFn func(), drainCh <-chan struct{}) stageFunc {
    return func(ctx context.Context) error {
        defer close(outCh)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer drainFn()

        for {
            select {
            case item, ok := <-inCh:
                if !ok {
                    return nil
                }
                if err := fn(ctx, item, func(out O) error {
                    select {
                    case outCh <- out:
                        return nil
                    case <-ctx.Done():
                        return ctx.Err()
                    case <-drainCh:
                        cooperativeDrain = true
                        return errDrained
                    }
                }); err != nil {
                    if err == errDrained {
                        return nil
                    }
                    return internal.WrapStageErr(name, err, 0)
                }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
}
```

- [ ] **Step 3: Update `flatMapSerial`, `flatMapConcurrent`, `flatMapOrdered` with the same pattern**

For each:
- Add `drainFn func(), drainCh <-chan struct{}` to the signature
- Replace `defer func() { go internal.DrainChan(inCh) }()` with the cooperative pattern
- Add `case <-drainCh: cooperativeDrain = true; return nil` to the outer receive select

For `flatMapConcurrent` and `flatMapOrdered` (which use worker goroutines): add `drainCh` to the dispatcher's inner dispatch select (the `func()` goroutine that reads from `inCh`).

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator_flatmap.go
git commit -m "feat(drain): convert FlatMap variants to cooperative drain"
```

---

## Task 5: Convert `batch.go` — Group 1 operators

**Files:**
- Modify: `batch.go`

Group 1 operators: `Batch`, `Unbatch`, `Window`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, `ChunkWhile`.
Group 2 operator: `BufferWith` (two inputs — handled separately in Task 12).

Convert all 7 Group 1 operators using the standard pattern.

- [ ] **Step 1: Convert `Batch`**

```go
var out *Pipeline[[]T]
build := func(rc *runCtx) chan []T {
    // ... existing channel/meta setup ...
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(ch)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        outbox := internal.NewBlockingOutbox(ch)
        var buf []T
        flush := func() error { /* unchanged */ }

        if cfg.batchTimeout == 0 {
            for {
                select {
                case item, ok := <-inCh:
                    if !ok { return flush() }
                    buf = append(buf, item)
                    if len(buf) >= size {
                        if err := flush(); err != nil { return err }
                    }
                case <-ctx.Done():
                    return ctx.Err()
                case <-drainCh:
                    cooperativeDrain = true
                    return nil
                }
            }
        }

        // timeout path:
        clk := cfg.clock
        if clk == nil { clk = internal.SystemClock{} }
        timer := clk.NewTimer(cfg.batchTimeout)
        defer timer.Stop()
        for {
            select {
            case item, ok := <-inCh:
                if !ok { return flush() }
                buf = append(buf, item)
                if len(buf) >= size {
                    timer.Reset(cfg.batchTimeout)
                    if err := flush(); err != nil { return err }
                }
            case <-timer.C():
                timer.Reset(cfg.batchTimeout)
                if err := flush(); err != nil { return err }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Apply the same pattern to `Unbatch`, `Window`, `SlidingWindow`, `SessionWindow`, `ChunkBy`, `ChunkWhile`**

Each gets: `var out`, `initDrainNotify`, `drainCh`, `cooperativeDrain` flag, `signalDrain`, and `case <-drainCh` in all select loops. The logic inside each operator is unchanged.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add batch.go
git commit -m "feat(drain): convert Batch/Window/Chunk operators to cooperative drain"
```

---

## Task 6: Convert `aggregate.go` — Group 1

**Files:**
- Modify: `aggregate.go`

Operators: `Scan`, `Reduce` (and any Reduce variants), `GroupByStream`, `FrequenciesByStream`, and all others with DrainChan calls (8 total at lines 41, 98, 160, 190, 261, 291, 373, 464).

- [ ] **Step 1: Convert `Scan` as the reference example**

```go
var out *Pipeline[S]
build := func(rc *runCtx) chan S {
    if existing := rc.getChan(id); existing != nil {
        return existing.(chan S)
    }
    inCh := p.build(rc)
    buf := rc.effectiveBufSize(cfg)
    ch := make(chan S, buf)
    m := meta
    m.buffer = buf
    m.getChanLen = func() int { return len(ch) }
    m.getChanCap = func() int { return cap(ch) }
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(ch)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        outbox := internal.NewBlockingOutbox(ch)
        state := initial

        for {
            select {
            case item, ok := <-inCh:
                if !ok { return nil }
                state = fn(state, item)
                if err := outbox.Send(ctx, state); err != nil { return err }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Apply the same pattern to all remaining operators in `aggregate.go` that have `go internal.DrainChan(inCh)` calls**

Each operator follows the identical four-step pattern. The internal logic (state accumulation, etc.) is unchanged.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add aggregate.go
git commit -m "feat(drain): convert aggregate operators to cooperative drain"
```

---

## Task 7: Convert `misc.go`, `middleware.go`, `notification.go`, `compat.go` — Group 1

**Files:**
- Modify: `misc.go`, `middleware.go`, `notification.go`, `compat.go`

Operators:
- `misc.go` Group 1: `DefaultIfEmpty`, `Timestamp`, `TimeInterval`, `Sort`, `SortBy` (lines 131, 197, 260, 329)
- `middleware.go` Group 1: `RateLimit` (line 101)
- `notification.go` Group 1: `Dematerialize` (line 149)
- `compat.go` Group 1: `WindowByTime` (line 339)

`misc.go` also has `Unzip` (line 416) — that's Group 3, handled in Task 14.

- [ ] **Step 1: Apply the standard Group 1 pattern to each operator listed above**

All follow the same four changes: `var out`, `initDrainNotify`, `drainCh`, `cooperativeDrain` flag + `signalDrain` + `drainCh` arm in select.

- [ ] **Step 2: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add misc.go middleware.go notification.go compat.go
git commit -m "feat(drain): convert misc/middleware/notification/compat operators to cooperative drain"
```

---

## Task 8: Convert `operator_transform.go` — Group 1

**Files:**
- Modify: `operator_transform.go`

Operators: `WithIndex`, `Pairwise`, `TakeEvery`, `DropEvery`, `MapEvery`, `Intersperse` (lines 57, 119, 181, 237, 294, 359).

- [ ] **Step 1: Apply the standard Group 1 pattern to all 6 operators**

All follow the four-step pattern. None use `Supervise` or inner loops — each is a simple outer `for { select { ... } }`.

- [ ] **Step 2: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add operator_transform.go
git commit -m "feat(drain): convert operator_transform operators to cooperative drain"
```

---

## Task 9: Convert `state_with.go` — Group 1

**Files:**
- Modify: `state_with.go`

Operators (6 DrainChan calls at lines 46, 105, 200, 435, 492, 583):
- `mapWithSerial`, `mapWithConcurrent`, `mapWithOrdered` — called from `MapWith`'s build
- `flatMapWithSerial`, `flatMapWithConcurrent`, `flatMapWithOrdered` — called from `FlatMapWith`'s build

These are helper functions, like the FlatMap variants. Update their signatures and the callers in `MapWith` and `FlatMapWith`.

- [ ] **Step 1: Add `var out` + drain setup to `MapWith`'s `build()` function**

```go
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    // ... existing setup ...
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)
    drainFn := func() { rc.signalDrain(p.id) }

    switch {
    case cfg.concurrency > 1 && cfg.ordered:
        stage = mapWithOrdered(inCh, ch, key, fn, cfg, hook, rc, drainFn, drainCh)
    case cfg.concurrency > 1:
        stage = mapWithConcurrent(inCh, ch, key, fn, cfg, hook, rc, drainFn, drainCh)
    default:
        stage = mapWithSerial(inCh, ch, key, fn, cfg, hook, rc, drainFn, drainCh)
    }
    // ...
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Update `mapWithSerial`, `mapWithConcurrent`, `mapWithOrdered` signatures**

Add `drainFn func(), drainCh <-chan struct{}` as the last two parameters. Apply the standard cooperative drain pattern inside each function:
- Replace DrainChan defer with cooperativeDrain flag
- Add drainFn defer
- Add `case <-drainCh: cooperativeDrain = true; return nil` to the main select

For concurrent/ordered variants (which use worker goroutines): add drainCh to the dispatcher select loop.

- [ ] **Step 3: Repeat for `FlatMapWith` and the three `flatMapWith*` helpers**

Same structure as MapWith. Apply the same signature update and cooperative drain pattern.

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add state_with.go
git commit -m "feat(drain): convert MapWith/FlatMapWith to cooperative drain"
```

---

## Task 10: Convert `state_withkey.go` — Group 1

**Files:**
- Modify: `state_withkey.go`

Operators (6 DrainChan calls at lines 100, 163, 284, 552, 611, 727):
- `mapWithKeySerial`, `mapWithKeyConcurrent`, `mapWithKeyOrdered`
- `flatMapWithKeySerial`, `flatMapWithKeyConcurrent`, `flatMapWithKeyOrdered`

Same structure as Task 9. Apply identically.

- [ ] **Step 1: Add `var out` + drain setup to `MapWithKey`'s `build()`**

```go
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    // ... existing setup ...
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)
    drainFn := func() { rc.signalDrain(p.id) }
    // route to mapWithKeySerial/Concurrent/Ordered with drainFn, drainCh
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Update all 6 helper function signatures and bodies**

Add `drainFn func(), drainCh <-chan struct{}` to each. Apply the cooperative drain pattern (cooperativeDrain flag, drainFn defer, drainCh select arm).

- [ ] **Step 3: Repeat for `FlatMapWithKey` and the three `flatMapWithKey*` helpers**

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add state_withkey.go
git commit -m "feat(drain): convert MapWithKey/FlatMapWithKey to cooperative drain"
```

---

## Task 11: Convert `advanced.go` Group 1 operators

**Files:**
- Modify: `advanced.go`

Group 1 operators: `SwitchMap` (line 63), `ExhaustMap` (line 207), `MapRecover` (line 419).
Group 3 operator: `MapResult` (line 348) — handled in Task 14.

- [ ] **Step 1: Apply the standard Group 1 pattern to `SwitchMap`, `ExhaustMap`, and `MapRecover`**

All three are single-input/single-output operators. Apply the four-step pattern: `var out`, `initDrainNotify`, `drainCh`, `cooperativeDrain` + `signalDrain` + `drainCh` arm.

- [ ] **Step 2: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add advanced.go
git commit -m "feat(drain): convert SwitchMap/ExhaustMap/MapRecover to cooperative drain"
```

---

## Task 12: Convert multi-input fan-in operators — `fan_combine.go` + `BufferWith` in `batch.go`

**Files:**
- Modify: `fan_combine.go`, `batch.go`

Operators:
- `fan_combine.go`: `ZipWith` (lines 58-59), `CombineLatestWith` (148-149), `WithLatestFromWith` (286-287), `SampleWith` (397-398)
- `batch.go`: `BufferWith` (lines 161-162)

Group 2 pattern: signal drain to BOTH inputs, monitor drainCh for own output.

- [ ] **Step 1: Convert `ZipWith` in `fan_combine.go`**

```go
var out *Pipeline[O]
build := func(rc *runCtx) chan O {
    // ... existing setup ...
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(ch)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(aCh)
                go internal.DrainChan(bCh)
            }
        }()
        defer func() { rc.signalDrain(a.id) }()
        defer func() { rc.signalDrain(b.id) }()

        outbox := internal.NewBlockingOutbox(ch)

        for {
            // Read from a
            var av A
            select {
            case v, ok := <-aCh:
                if !ok { return nil }
                av = v
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }

            // Read from b
            var bv B
            select {
            case v, ok := <-bCh:
                if !ok { return nil }
                bv = v
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }

            val, err := fn(ctx, av, bv)
            if err != nil {
                return internal.WrapStageErr(cfg.name, err, 0)
            }
            if err := outbox.Send(ctx, val); err != nil {
                return err
            }
        }
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Apply Group 2 pattern to `CombineLatestWith`, `WithLatestFromWith`, `SampleWith`**

Each signals drain to both input IDs and monitors drainCh for its own output. Add drainCh to every select arm in the stage func.

- [ ] **Step 3: Convert `BufferWith` in `batch.go`**

Same Group 2 pattern. `BufferWith` reads from `srcCh` (input `p.id`) and `selCh` (input `closingSelector.id`). Add `rc.initDrainNotify(id, out.consumerCount.Load())`, drain both inputs on exit.

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add fan_combine.go batch.go
git commit -m "feat(drain): convert ZipWith/CombineLatest/WithLatestFrom/SampleWith/BufferWith to cooperative drain"
```

---

## Task 13: Convert `Merge` in `fan_out.go`, `SequenceEqual` in `collect.go`, `TakeUntil`/`SkipUntil` in `operator_take.go`

**Files:**
- Modify: `fan_out.go`, `collect.go`, `operator_take.go`

- [ ] **Step 1: Convert `Merge` in `fan_out.go` (Group 2, N inputs)**

`Merge` uses a `sync.WaitGroup` with N goroutines each reading from one input channel.

```go
var out *Pipeline[T]
build := func(rc *runCtx) chan T {
    // ... existing setup ...
    rc.setChan(id, ch)
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(ch)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                for _, ic := range inChans {
                    go internal.DrainChan(ic)
                }
            }
        }()
        for _, p := range pipelines {
            p := p
            defer func() { rc.signalDrain(p.id) }()
        }

        outbox := internal.NewBlockingOutbox(ch)
        innerCtx, cancel := context.WithCancel(ctx)
        defer cancel()
        var wg sync.WaitGroup

        for _, ic := range inChans {
            ic := ic
            wg.Add(1)
            go func() {
                defer wg.Done()
                for {
                    select {
                    case item, ok := <-ic:
                        if !ok { return }
                        if err := outbox.Send(innerCtx, item); err != nil { return }
                    case <-innerCtx.Done():
                        return
                    }
                }
            }()
        }

        // Wait for drainCh or all inputs to close
        doneCh := make(chan struct{})
        go func() {
            wg.Wait()
            close(doneCh)
        }()
        select {
        case <-doneCh:
            return ctx.Err()
        case <-drainCh:
            cooperativeDrain = true
            cancel() // stop worker goroutines
            wg.Wait()
            return nil
        case <-ctx.Done():
            cancel()
            wg.Wait()
            return ctx.Err()
        }
    }
    rc.add(stage, m)
    return ch
}
out = newPipeline(id, meta, build)
return out
```

- [ ] **Step 2: Convert `SequenceEqual` in `collect.go` (Group 2 terminal)**

`SequenceEqual` is a terminal (doesn't produce a Pipeline). It uses DrainChan for cleanup. Replace with signalDrain:

```go
stage := func(stageCtx context.Context) error {
    cooperativeDrain := false
    defer func() {
        if !cooperativeDrain {
            go internal.DrainChan(aCh)
            go internal.DrainChan(bCh)
        }
    }()
    defer func() { rc.signalDrain(a.id) }()
    defer func() { rc.signalDrain(b.id) }()
    // ... rest of stage unchanged ...
}
```

Note: `SequenceEqual` is a terminal so it does not call `initDrainNotify` (it has no output channel of its own). It just signals drain to its inputs.

- [ ] **Step 3: Convert `TakeUntil` and `SkipUntil` in `operator_take.go` (Group 2)**

Both have two inputs (main pipeline + boundary pipeline). Add `var out`, `initDrainNotify`, `drainCh`, signal drain to BOTH `p.id` and `boundary.id`, and add `case <-drainCh` to the main select.

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add fan_out.go collect.go operator_take.go
git commit -m "feat(drain): convert Merge/SequenceEqual/TakeUntil/SkipUntil to cooperative drain"
```

---

## Task 14: Convert `Unzip` in `misc.go` and `MapResult` in `advanced.go` — Group 3

**Files:**
- Modify: `misc.go`, `advanced.go`

Both produce two output channels from one input. Use `initMultiOutputDrainNotify`.

- [ ] **Step 1: Convert `Unzip` in `misc.go`**

```go
var aP *Pipeline[A]
var bP *Pipeline[B]

sharedBuild := func(rc *runCtx) (chan A, chan B) {
    if existing := rc.getChan(aID); existing != nil {
        return existing.(chan A), rc.getChan(bID).(chan B)
    }
    inCh := p.build(rc)
    buf := rc.effectiveBufSize(cfg)
    aCh := make(chan A, buf)
    bCh := make(chan B, buf)
    // ... meta setup, rc.setChan ...

    totalConsumers := aP.consumerCount.Load() + bP.consumerCount.Load()
    rc.initMultiOutputDrainNotify([]int64{aID, bID}, totalConsumers)
    drainCh := rc.drainCh(aID)

    stage := func(ctx context.Context) error {
        defer close(aCh)
        defer close(bCh)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        aBox := internal.NewBlockingOutbox(aCh)
        bBox := internal.NewBlockingOutbox(bCh)

        for {
            select {
            case pair, ok := <-inCh:
                if !ok { return nil }
                if err := aBox.Send(ctx, pair.First); err != nil { return err }
                if err := bBox.Send(ctx, pair.Second); err != nil { return err }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return aCh, bCh
}

aP = newPipeline(aID, aMeta, func(rc *runCtx) chan A {
    a, _ := sharedBuild(rc)
    return a
})
bP = newPipeline(bID, bMeta, func(rc *runCtx) chan B {
    _, b := sharedBuild(rc)
    return b
})
return aP, bP
```

- [ ] **Step 2: Apply the same Group 3 pattern to `MapResult` in `advanced.go`**

`MapResult` produces `(*Pipeline[O], *Pipeline[ErrItem[I]])`. Declare `var okP *Pipeline[O]` and `var errP *Pipeline[ErrItem[I]]` before `sharedBuild`. Use `initMultiOutputDrainNotify([]int64{okID, errID}, okP.consumerCount.Load()+errP.consumerCount.Load())`.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add misc.go advanced.go
git commit -m "feat(drain): convert Unzip and MapResult to cooperative drain (Group 3)"
```

---

## Task 15: Convert `fan_out.go` fan-out operators — Group 3

**Files:**
- Modify: `fan_out.go`

Operators: `Partition`, `Broadcast`/`BroadcastN`, `Balance`, `KeyedBalance`, `Share`.

- [ ] **Step 1: Convert `Partition`**

```go
var trueP *Pipeline[T]
var falseP *Pipeline[T]

sharedBuild := func(rc *runCtx) (chan T, chan T) {
    if existing := rc.getChan(id); existing != nil {
        return existing.(chan T), rc.getChan(falseID).(chan T)
    }
    inCh := p.build(rc)
    buf := rc.effectiveBufSize(cfg)
    trueC := make(chan T, buf)
    falseC := make(chan T, buf)
    // ... meta, rc.setChan ...

    totalConsumers := trueP.consumerCount.Load() + falseP.consumerCount.Load()
    rc.initMultiOutputDrainNotify([]int64{id, falseID}, totalConsumers)
    drainCh := rc.drainCh(id)

    stage := func(ctx context.Context) error {
        defer close(trueC)
        defer close(falseC)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        trueBox := internal.NewBlockingOutbox(trueC)
        falseBox := internal.NewBlockingOutbox(falseC)

        for {
            select {
            case item, ok := <-inCh:
                if !ok { return nil }
                var err error
                if pred(item) {
                    err = trueBox.Send(ctx, item)
                } else {
                    err = falseBox.Send(ctx, item)
                }
                if err != nil { return err }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return trueC, falseC
}

trueP = newPipeline(id, trueMeta, func(rc *runCtx) chan T {
    t, _ := sharedBuild(rc)
    return t
})
falseP = newPipeline(falseID, falseMeta, func(rc *runCtx) chan T {
    _, f := sharedBuild(rc)
    return f
})
return trueP, falseP
```

- [ ] **Step 2: Convert `BroadcastN` (N outputs)**

```go
out := make([]*Pipeline[T], n)
// ... ID and meta setup (unchanged) ...

sharedBuild := func(rc *runCtx) []chan T {
    if existing := rc.getChan(ids[0]); existing != nil {
        chans := make([]chan T, n)
        for i, id := range ids { chans[i] = rc.getChan(id).(chan T) }
        return chans
    }
    inCh := p.build(rc)
    buf := rc.effectiveBufSize(cfg)
    chans := make([]chan T, n)
    for i := range chans { chans[i] = make(chan T, buf) }
    // ... meta, rc.setChan for each ...

    totalConsumers := int32(0)
    for _, op := range out {
        totalConsumers += op.consumerCount.Load()
    }
    rc.initMultiOutputDrainNotify(ids, totalConsumers)
    drainCh := rc.drainCh(ids[0])

    stage := func(ctx context.Context) error {
        defer func() {
            for _, c := range chans { close(c) }
        }()
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer func() { rc.signalDrain(p.id) }()

        outboxes := make([]internal.Outbox[T], len(chans))
        for i, c := range chans {
            outboxes[i] = internal.NewBlockingOutbox(c)
        }
        for {
            select {
            case item, ok := <-inCh:
                if !ok { return nil }
                for _, ob := range outboxes {
                    if err := ob.Send(ctx, item); err != nil { return err }
                }
            case <-ctx.Done():
                return ctx.Err()
            case <-drainCh:
                cooperativeDrain = true
                return nil
            }
        }
    }
    rc.add(stage, m)
    return chans
}

for i := range out {
    i := i
    out[i] = newPipeline(ids[i], metas[i], func(rc *runCtx) chan T {
        return sharedBuild(rc)[i]
    })
}
return out
```

- [ ] **Step 3: Apply the same Group 3 pattern to `Balance` and `KeyedBalance`**

Both have the same structure as `BroadcastN`. Replace `go internal.DrainChan(inCh)` with the cooperative pattern, add `initMultiOutputDrainNotify` using the N output pipeline consumer counts, add `case <-drainCh`.

- [ ] **Step 4: Convert `Share`**

`Share` is dynamic — branches are registered via `subscribe()` calls. Add a `branchPipelines []*Pipeline[T]` slice alongside `branches []branchInfo`. In the `subscribe()` factory, append the newly created pipeline to `branchPipelines`.

In `sharedBuild` (inside `once.Do`):
```go
// Compute total consumers across all branch pipelines
totalConsumers := int32(0)
branchIDs := make([]int64, len(bs))
for i, b := range bs {
    branchIDs[i] = b.id
}
mu.Lock()
bps := make([]*Pipeline[T], len(branchPipelines))
copy(bps, branchPipelines)
mu.Unlock()
for _, bp := range bps {
    totalConsumers += bp.consumerCount.Load()
}
rc.initMultiOutputDrainNotify(branchIDs, totalConsumers)
drainCh := rc.drainCh(branchIDs[0])
```

Add `case <-drainCh: cooperativeDrain = true; return nil` to the stage select. Add cooperative drain pattern for the input.

- [ ] **Step 5: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add fan_out.go
git commit -m "feat(drain): convert Partition/Broadcast/Balance/KeyedBalance/Share to cooperative drain"
```

---

## Task 16: Convert `operator_map.go` — `mapConcurrent` and `mapOrdered` (Group 5)

**Files:**
- Modify: `operator_map.go`

The `Map` build already calls `rc.initDrainNotify` for the serial paths (lines 110, 115). Extend to the concurrent/ordered paths. Then update the helper function signatures.

- [ ] **Step 1: In `Map`'s build switch, add drain setup for concurrent branches**

```go
switch {
case cfg.concurrency > 1 && cfg.ordered:
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainFn := func() { rc.signalDrain(p.id) }
    drainCh := rc.drainCh(id)
    stage = mapOrdered(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
case cfg.concurrency > 1:
    rc.initDrainNotify(id, out.consumerCount.Load())
    drainFn := func() { rc.signalDrain(p.id) }
    drainCh := rc.drainCh(id)
    stage = mapConcurrent(inCh, ch, actualFn, cfg, hook, drainFn, drainCh)
// serial paths already have their initDrainNotify calls
```

- [ ] **Step 2: Update `mapConcurrent` signature and body**

```go
func mapConcurrent[I, O any](inCh <-chan I, outCh chan O, fn func(context.Context, I) (O, error), cfg stageConfig, hook internal.Hook, drainFn func(), drainCh <-chan struct{}) stageFunc {
    // ...
    return func(ctx context.Context) error {
        defer close(outCh)
        cooperativeDrain := false
        defer func() {
            if !cooperativeDrain {
                go internal.DrainChan(inCh)
            }
        }()
        defer drainFn()
        // ...
        inner := func() error {
            innerCtx, cancel := context.WithCancel(ctx)
            defer cancel()
            // ...
            func() {
                for {
                    select {
                    case item, ok := <-inCh:
                        if !ok { return }
                        select {
                        case sem <- struct{}{}:
                        case <-innerCtx.Done(): return
                        }
                        // ... spawn worker goroutine ...
                    case <-innerCtx.Done():
                        return
                    case <-drainCh:
                        cooperativeDrain = true
                        cancel()
                        return
                    }
                }
            }()
            wg.Wait()
            // ...
        }
        return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
    }
}
```

- [ ] **Step 3: Update `mapOrdered` signature and body with the same pattern**

`mapOrdered` has a similar dispatcher + worker structure. Add `drainFn` and `drainCh` parameters, apply the cooperative drain pattern to the dispatcher select loop.

- [ ] **Step 4: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator_map.go
git commit -m "feat(drain): convert mapConcurrent and mapOrdered to cooperative drain"
```

---

## Task 17: Convert fusionEntries in `operator_map.go` and `operator_filter.go` — Group 5

**Files:**
- Modify: `operator_map.go`, `operator_filter.go`

The fusionEntry fallback path runs when a fused chain has a non-fused upstream. It reads from `inCh` (the non-fused upstream's output channel) and currently spawns `go internal.DrainChan(inCh)` when exiting.

The fusionEntry is not a standard stage — it's a function that composes into the terminal (ForEach). The `inCh` here is the upstream pipeline's channel. We need to call `signalDrain(p0.id)` instead of DrainChan.

For the fusionEntry to call `signalDrain`, it needs access to `rc`. The fusionEntry signature is `func(rc *runCtx, sink func(context.Context, O) error) stageFunc`, so `rc` is already available.

- [ ] **Step 1: Convert the `Map` fusionEntry fallback in `operator_map.go`**

The fusionEntry fallback (around line 148) currently has:
```go
inCh := p0.build(rc)
return func(ctx context.Context) error {
    defer func() { go internal.DrainChan(inCh) }()
    // ...
}
```

Update to:
```go
inCh := p0.build(rc)
rc.initDrainNotify(p0.id, /* consumerCount */ 1) // fusionEntry has exactly 1 consumer (the fused stage)
return func(ctx context.Context) error {
    cooperativeDrain := false
    defer func() {
        if !cooperativeDrain {
            go internal.DrainChan(inCh)
        }
    }()
    defer func() { rc.signalDrain(p0.id) }()
    // Add drainCh monitoring:
    drainCh := rc.drainCh(p0.id)
    // ...
    for {
        item, ok := <-inCh
        if !ok { return nil }
        // ... micro-batch loop ...
        select {
        // existing micro-batch refill select already has a default case
        }
        // process batch items...
        if ctx.Err() != nil { return ctx.Err() }
        // Check drainCh between batches:
        select {
        case <-drainCh:
            cooperativeDrain = true
            return nil
        default:
        }
    }
}
```

Note: the fusionEntry's inner loop uses a blocking receive followed by non-blocking refill. Add a drainCh non-blocking check after processing each batch (the select+default pattern above). For the initial blocking receive, convert to a select that includes drainCh.

- [ ] **Step 2: Apply the same change to the `Filter` fusionEntry fallback in `operator_filter.go` (lines 115-160)**

Same structure: fusionEntry fallback reads from `p0.build(rc)`. Add `initDrainNotify`, cooperative drain pattern, drainCh check between batches.

- [ ] **Step 3: Run tests**

```bash
task test:race
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add operator_map.go operator_filter.go
git commit -m "feat(drain): convert Map and Filter fusionEntry fallback paths to cooperative drain"
```

---

## Task 18: Extend `drain_test.go` with goroutine-count assertions

**Files:**
- Modify: `drain_test.go`

The existing `drain_test.go` has a goroutine count test for the converted operators. Add assertions for fan-out, multi-input, and source operators.

- [ ] **Step 1: Add a goroutine-count test for `Merge` + `Take(1)` (multi-input, fan-in)**

```go
func TestCooperativeDrainMerge(t *testing.T) {
    baseline := runtime.NumGoroutine()
    for range 50 {
        src1 := kitsune.Repeatedly(func(_ context.Context) (int, error) { return 1, nil })
        src2 := kitsune.Repeatedly(func(_ context.Context) (int, error) { return 2, nil })
        p := kitsune.Take(kitsune.Merge(src1, src2), 1)
        _, err := kitsune.Collect(t.Context(), p)
        if err != nil {
            t.Fatal(err)
        }
    }
    runtime.GC()
    after := runtime.NumGoroutine()
    // Allow 2 goroutines above baseline (test framework overhead).
    if after-baseline > 2 {
        t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
    }
}
```

- [ ] **Step 2: Add a goroutine-count test for `Broadcast` + two `Take(1)` consumers (fan-out)**

```go
func TestCooperativeDrainBroadcast(t *testing.T) {
    baseline := runtime.NumGoroutine()
    for range 50 {
        src := kitsune.Repeatedly(func(_ context.Context) (int, error) { return 1, nil })
        branches := kitsune.Broadcast(src, 2)
        p0 := kitsune.Take(branches[0], 1)
        p1 := kitsune.Take(branches[1], 1)
        runner := kitsune.MergeRunners(p0.Runner(), p1.Runner())
        if err := runner.Run(t.Context()); err != nil {
            t.Fatal(err)
        }
    }
    runtime.GC()
    after := runtime.NumGoroutine()
    if after-baseline > 2 {
        t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
    }
}
```

- [ ] **Step 3: Add a goroutine-count test for `FromSlice` + `Take(1)` (source)**

```go
func TestCooperativeDrainSource(t *testing.T) {
    baseline := runtime.NumGoroutine()
    items := make([]int, 1000)
    for i := range items { items[i] = i }
    for range 50 {
        p := kitsune.Take(kitsune.FromSlice(items), 1)
        _, err := kitsune.Collect(t.Context(), p)
        if err != nil {
            t.Fatal(err)
        }
    }
    runtime.GC()
    after := runtime.NumGoroutine()
    if after-baseline > 2 {
        t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
    }
}
```

- [ ] **Step 4: Run the new tests**

```bash
task test:race
```

Expected: all tests pass. If any goroutine leak tests fail, trace which operator still uses DrainChan and convert it.

- [ ] **Step 5: Commit**

```bash
git add drain_test.go
git commit -m "test(drain): add goroutine-count assertions for fan-out, merge, and source operators"
```

---

## Task 19: Update roadmap + final verification

**Files:**
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Run the full test suite**

```bash
task test:all
```

Expected: all tests pass, no races.

- [ ] **Step 2: Run the drain benchmark to confirm improvement**

```bash
go test -bench=BenchmarkDrainBurst -benchmem -count=3 ./...
```

Note the goroutine count and ns/op — should show improvement over pre-rollout baseline.

- [ ] **Step 3: Confirm zero remaining DrainChan calls (except in the internal package itself)**

```bash
grep -rn "go internal.DrainChan" *.go
```

Expected: zero matches. (The `internal/` package still has the `DrainChan` function definition — that's correct.)

- [ ] **Step 4: Mark roadmap item complete**

In `doc/roadmap.md`, find the line:
```
- [ ] **Cooperative drain: full operator rollout**
```
Change to:
```
- [x] **Cooperative drain: full operator rollout**
```

- [ ] **Step 5: Commit**

```bash
git add doc/roadmap.md
git commit -m "docs(roadmap): mark cooperative drain full rollout complete"
```
