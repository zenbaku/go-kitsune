# Idempotency-key-driven dedupe for `Effect` — design

**Status:** ready for implementation
**Date:** 2026-04-26
**Roadmap item:** Long-term → "Idempotency-key-driven deduplication for `Effect`"

---

## Problem

`EffectPolicy.Idempotent bool` and `EffectPolicy.IdempotencyKey func(any) string` are captured by the operator today but inert: the v1 godoc says "the operator does not de-duplicate retries against a backing store." Real-world Effect use cases (sending notifications, charging cards, posting to message queues) commonly need at-most-once semantics within a run, especially when the same upstream item arrives more than once (Kafka consumer rebalance, replay-from-snapshot, fan-in deduplication).

## Goal

Wire `EffectPolicy.IdempotencyKey` to a configurable `IdempotencyStore` so that, when both are set, an item whose key has already been seen is skipped: the effect function is not called and a deduped outcome is emitted downstream. By default a per-Run in-memory store is attached; users can supply a persistent store (Redis SETNX, etc.) for cross-Run dedupe.

## Non-goals

- **No "replay the prior result" semantics.** Deduped items emit a zero `Result` with `Deduped: true`. We do not store and replay `R`. (See "alternatives considered.")
- **No retry-of-failed-keys.** Once a key is recorded (on `Add` returning `firstTime=true`), subsequent items with the same key are deduped even if the original attempt eventually failed. Documented v1 limitation.
- **No automatic key derivation.** Users must supply `IdempotencyKey func(any) string`. We do not hash items by default.
- **No changes to `RunOutcome` derivation.** Deduped items do not count as failures; the existing per-Effect-stage `failure` counter continues to drive `RunOutcome`.

## Public surface

### New interface

```go
// IdempotencyStore tracks idempotency keys for Effect dedupe. Implementations
// must be safe for concurrent use. The default in-process implementation is
// attached automatically when an Effect stage is configured with Idempotent
// and IdempotencyKey but no explicit IdempotencyStore.
//
// Cross-Run dedupe (e.g. survives process restart) requires a user-supplied
// implementation backed by a persistent store such as Redis (using SETNX).
type IdempotencyStore interface {
    // Add atomically records key as seen. It returns true if key was newly
    // recorded (first time seen) and false if the key was already present.
    // An error indicates the store could not be queried; the calling Effect
    // treats this as a per-item failure.
    Add(ctx context.Context, key string) (firstTime bool, err error)
}
```

Lives in a new file `idempotency.go` to keep `effect.go` focused.

### `EffectPolicy` extension

```go
type EffectPolicy struct {
    // ... existing fields ...

    // IdempotencyStore overrides the default per-Run in-memory store.
    // Use when dedupe must survive across Runs (e.g. backed by Redis SETNX).
    // When nil and Idempotent is true with a non-nil IdempotencyKey, an
    // in-process per-Run store is attached automatically.
    IdempotencyStore IdempotencyStore
}
```

`applyEffect` is updated to copy this field into `effectConfig`.

### `EffectOutcome` extension

```go
type EffectOutcome[I, R any] struct {
    Input   I
    Result  R
    Err     error
    Applied bool
    // Deduped is true when the effect was skipped because the item's
    // idempotency key matched a previously-recorded invocation. When
    // Deduped is true, Applied is false, Err is nil, and Result is the
    // zero value of R.
    Deduped bool
}
```

`TryEffect` continues to split on `Err`: deduped outcomes (Err nil) flow to the "ok" branch, which is the desired behavior.

### `EffectStats` extension

```go
type EffectStats struct {
    Required bool
    Success  int64
    Failure  int64
    // Deduped is the count of items whose idempotency key matched a
    // previously-recorded invocation; for these items the effect function
    // was not called.
    Deduped  int64
}
```

The JSON tag is `"deduped"`.

## Architecture

### Stage-build wiring

When an Effect stage builds, after assembling `localCfg` and the existing per-stage state, resolve the idempotency store:

```go
var idemStore IdempotencyStore
if localCfg.idempotent && localCfg.idempotencyKey != nil {
    if localCfg.idempotencyStore != nil {
        idemStore = localCfg.idempotencyStore
    } else {
        idemStore = newMemoryIdempotencyStore()
    }
}
```

`idemStore` is captured by the stage closure and shared by all worker goroutines under `Concurrency(n)`. The default `memoryIdempotencyStore` is per-stage (one per Run); cross-stage and cross-Run dedupe requires the user-supplied path.

### Stage-loop integration

In `effect.go`'s stage `case item, ok := <-inCh:` block, the existing call to `runEffectAttempts` is wrapped in a dedupe check:

```go
var outcome EffectOutcome[I, R]
outcome.Input = item

if !dryRun {
    skipped := false
    if idemStore != nil {
        key := localCfg.idempotencyKey(item)
        if key != "" {
            firstTime, err := idemStore.Add(ctx, key)
            if err != nil {
                outcome.Err = err
                hook.OnItem(ctx, stageName, 0, err)
                rc.recordEffectOutcome(id, false)
                errored++
                skipped = true
            } else if !firstTime {
                outcome.Deduped = true
                hook.OnItem(ctx, stageName, 0, nil)
                rc.recordEffectDeduped(id)
                deduped++
                skipped = true
            }
        }
    }
    if !skipped {
        start := time.Now()
        outcome = runEffectAttempts(ctx, item, fn, localCfg)
        hook.OnItem(ctx, stageName, time.Since(start), outcome.Err)
        rc.recordEffectOutcome(id, outcome.Applied)
        if outcome.Err != nil {
            errored++
        } else {
            processed++
        }
    }
}
```

A new local `deduped int64` counter mirrors `processed` and `errored`. It is not currently passed to `OnStageDone` (which has a fixed signature `(processed, errors)`); the deduped count is captured via `recordEffectDeduped` and surfaces via `EffectStats.Deduped`.

### `runCtx` plumbing

`pipeline.go` gains:

```go
type effectStat struct {
    name     string
    required bool
    success  atomic.Int64
    failure  atomic.Int64
    deduped  atomic.Int64  // NEW
}
```

And:

```go
func (rc *runCtx) recordEffectDeduped(id int64) {
    if s, ok := rc.effectStats[id]; ok {
        s.deduped.Add(1)
    }
}
```

### `EffectStats` projection

`kitsune.go`'s projection reads the new field:

```go
summary.EffectStats[s.name] = EffectStats{
    Required: s.required,
    Success:  s.success.Load(),
    Failure:  s.failure.Load(),
    Deduped:  s.deduped.Load(),
}
```

## Default in-memory store

In `idempotency.go`:

```go
type memoryIdempotencyStore struct {
    mu   sync.Mutex
    seen map[string]struct{}
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
    return &memoryIdempotencyStore{seen: make(map[string]struct{})}
}

func (s *memoryIdempotencyStore) Add(_ context.Context, key string) (bool, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, ok := s.seen[key]; ok {
        return false, nil
    }
    s.seen[key] = struct{}{}
    return true, nil
}
```

The mutex is acquired once per `Add` and released before `Add` returns. Atomic check-and-set is implicit. Race-safe under `Concurrency(n>1)`.

## Edge cases

| Case | Behavior |
|---|---|
| `Idempotent: false` (default) | Skip the entire dedupe path. Existing behavior. |
| `Idempotent: true` but `IdempotencyKey: nil` | Skip the dedupe path. Documented as a no-op configuration. |
| `idempotencyKey(item)` returns `""` | Skip dedupe for this item; run normally. Allows per-item opt-out. |
| `idemStore.Add` returns error | Treat as per-item failure: outcome with `Err: storeErr`, fire `OnItem(err)`, increment failure counter. Effect fn is not invoked. |
| User-supplied `IdempotencyStore` | Replaces the default. Same `Add` contract. |
| Concurrent items with same key | `Add` is atomic; exactly one returns `firstTime=true`. The rest are deduped. |
| Failed first attempt with retries exhausted | The key was recorded on `Add`. A subsequent item with the same key is deduped even though the original failed. **v1 limitation; documented.** |
| Dry-run mode (`DryRun()`) | The existing `if !dryRun` guard wraps the entire dedupe + run block. Dry-run unchanged. |
| Pipeline restart with default in-memory store | All keys forgotten. As designed. |

## Alternatives considered

**Replay-the-result semantics.** Store the `Result` value alongside each key, and on dedupe hit return that result. Requires `R` to be serializable (codec) or kept type-erased in-process. Adds complexity without addressing the v1 use case (skip-on-duplicate). Defer until a user case demands cached replay.

**`Add`-only after success.** Record the key after `runEffectAttempts` returns success. Avoids the "failed first attempt poisons future arrivals" limitation. Requires a separate "in-flight" set to prevent two concurrent items both running `fn` for the same key. Doable but more complex; defer to v2.

**Reuse `DedupSet` interface.** `DedupSet` has `Contains` and `Add` as separate methods; under concurrency this requires external synchronization (or compare-and-set semantics that the interface doesn't promise). Adding atomic semantics would either bloat `DedupSet` or duplicate the interface. A dedicated `IdempotencyStore` interface is clearer about its atomic-add contract.

## Testing

Five tests in a new `effect_idempotency_test.go`:

1. **Default in-memory store dedupes within a Run.** Source emits `[1, 2, 1, 3, 2]`. Key fn returns the int as a string. Assert `fn` is called exactly 3 times (for 1, 2, 3); the 4th and 5th items emit deduped outcomes. `summary.EffectStats[name].Deduped == 2`, `Success == 3`, `Failure == 0`.
2. **Empty key opts out per-item.** Key fn returns `""` for `v == 1`, otherwise `string(v)`. Source `[1, 1, 1, 2, 2]`. Assert `fn` is called 4 times (the three `1`s plus the first `2`); the second `2` is deduped. `Deduped == 1`.
3. **User-supplied `IdempotencyStore` is used.** Fake store implements `IdempotencyStore` with a counter. Pass via `EffectPolicy.IdempotencyStore`. Assert `fake.AddCalls == len(input)`.
4. **`Add` error counts as failure.** Fake store returns error on every call. Assert outcome has `Err`, `EffectStats.Failure == len(input)`, `fn` is not called (track via a counter the user fn increments).
5. **Concurrent dedupe is race-free.** `Concurrency(4)` with input `[1, 1, 1, ..., 1]` (50 duplicates). Run under `-race`. Assert `fn` is called exactly once. `Deduped == 49`.

`task test:race` for the concurrency check.

## File touch list

| File | Change |
|---|---|
| `idempotency.go` (new) | `IdempotencyStore` interface; `memoryIdempotencyStore` and `newMemoryIdempotencyStore` |
| `effect.go` | `EffectOutcome.Deduped`; `EffectPolicy.IdempotencyStore`; `effectConfig.idempotencyStore`; `applyEffect` copies the field; stage build resolves `idemStore`; stage loop wraps `runEffectAttempts` with dedupe check |
| `pipeline.go` | `effectStat.deduped atomic.Int64`; `recordEffectDeduped(id)` helper |
| `runsummary.go` | `EffectStats.Deduped int64` field; updated godoc |
| `kitsune.go` | Projection reads `s.deduped.Load()` into `Deduped` |
| `effect_idempotency_test.go` (new) | Five tests above |
| `doc/operators.md` (Effect section) | Document idempotency-key dedupe and the new `Deduped` outcome field |
| `doc/run-summary.md` | Update the `EffectStats` paragraph to mention `Deduped` |
| `doc/roadmap.md` | Mark the item `[x]` |
| Memory: `project_higher_level_authoring.md` | Note that idempotency-key dedupe is now wired |

## Spec self-review

- **Placeholders:** none. Every section has concrete content.
- **Internal consistency:** the `Deduped` field appears on `EffectOutcome`, on `EffectStats`, and as a counter in `effectStat`. The flow: `Add` returns `firstTime=false` → `outcome.Deduped = true` → `recordEffectDeduped(id)` → `effectStat.deduped++` → `summary.EffectStats[name].Deduped`. Each step references the next.
- **Scope:** single plan. New file (`idempotency.go`), one new test file, four file edits, three doc edits.
- **Ambiguity:**
  - "Empty key" is documented as `""` returned by `IdempotencyKey(item)`.
  - Failed-first-attempt limitation is called out explicitly.
  - The dedupe check happens before the retry loop, not inside it: each upstream item arrival is one dedupe-eligible event, regardless of how many retries `runEffectAttempts` performs.
