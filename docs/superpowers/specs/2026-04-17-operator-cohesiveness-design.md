---
title: Operator Cohesiveness Audit
date: 2026-04-17
status: approved
---

# Operator Cohesiveness Audit

A comprehensive review of the go-kitsune operator surface, driven by the goal of having a clear, consistent strategy before adding more operators. Pre-1.0; breaking changes are in scope.

---

## Motivation

The library has accumulated operators from different design eras. Some naming conventions are inconsistent, some operators exist only for historical compatibility, and the boundary between "streaming" and "terminal" was blurry. This document establishes clear principles and specifies the concrete changes needed to align the entire surface.

---

## Core Semantic Model

Every operator fits one of four behaviors:

| Behavior | Description | Examples |
|---|---|---|
| **Streaming** | Emits output for each input item; no barrier | `Map`, `Filter`, `FlatMap`, `Tap` |
| **Running** | Maintains accumulating state; emits an updated snapshot on every item | `Scan`, `RunningFrequencies`, `CombineLatest` |
| **Buffering** | Must consume all input before emitting; returns `*Pipeline[T]` | `Sort`, `TakeRandom` (after §4.2), `GroupBy` |
| **Terminal** | Drains the pipeline; returns a plain Go value | `Collect`, `Single`, `Count`, `Sum`, `Any`, `All` |

The key distinction users care about is: **does output appear as items arrive, or only after the source closes?** Documentation should use "buffers all input" as a callout on buffering operators rather than inventing a separate category.

---

## 1. Naming Conventions

### 1.1 `Running*` prefix for per-item running aggregates

Operators that maintain accumulating state and emit a snapshot per item use the `Running` prefix. The `*Stream` suffix is removed — it is backwards (it implies the non-stream variant is the default, when in this library pipelines are the default).

| Current | New |
|---|---|
| `FrequenciesStream` | `RunningFrequencies` |
| `FrequenciesByStream` | `RunningFrequenciesBy` |
| `CountBy` (compat.go) | `RunningCountBy` |
| `SumBy` (compat.go) | `RunningSumBy` |

`GroupByStream` and the terminal `GroupBy(ctx, p, keyFn)` are both removed and replaced by a new buffering pipeline operator `GroupBy` (see §7).

### 1.2 `Drop*` prefix consistency

`Drop`, `DropWhile`, `DropEvery` are established. Two stragglers are aligned:

| Current | New |
|---|---|
| `SkipLast` | `DropLast` |
| `SkipUntil` | `DropUntil` |

`Skip()` in `config.go` is a deprecated alias for `ActionDrop` — remove it (see §7).

### 1.3 `LatestFrom` (remove `With` prefix)

The `With` prefix is an Angular/RxJS convention that does not fit the library. Every other multi-input operator is verb-first (`CombineLatest`, `ZipWith`, `Merge`):

| Current | New |
|---|---|
| `WithLatestFrom` | `LatestFrom` |
| `WithLatestFromWith` | `LatestFromWith` |

---

## 2. Dedup / Distinct Semantics

The distinction between `Distinct` and `Dedupe` was previously muddled. The correct model:

### `Distinct` / `DistinctBy`
SQL semantics: strict global uniqueness, no expiry, no re-emission. Items are deduplicated against an in-memory set that lives for the lifetime of the pipeline.

- Remove undocumented `WithDedupSet` code path from `Distinct`/`DistinctBy`. Accepting a custom backend here implies "Distinct with expiry," which is conceptually `Dedupe`, not `Distinct`.

### `Dedupe` / `DedupeBy`
Streaming dedup with configurable memory. Can expire and re-emit after a window.

- **Change default**: from consecutive-only to global in-memory (no expiry). This aligns with user expectation: calling `Dedupe` without options should suppress all duplicates, not just consecutive ones.
- **Add `DedupeWindow(n int) StageOption`**: count-based sliding window. `DedupeWindow(1)` = consecutive dedup (the old default).
- Keep `WithDedupSet` for custom backends (TTL sets, Bloom filters, Redis).

---

## 3. Batch API Redesign

### Remove positional `size` parameter

The current `Batch(p, size int, opts...)` conflates "flush by count" with the function signature. The size parameter is not a `StageOption`, so it cannot be combined symmetrically with other flush triggers.

**New signature:**
```go
func Batch[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[[]T]
```

At least one flush trigger is required. Passing neither panics at construction with a clear message.

### New StageOptions

```go
// BatchCount flushes when the batch accumulates n items.
func BatchCount(n int) StageOption

// BatchMeasure flushes when the sum of measureFn across buffered items
// reaches or exceeds max. measureFn is called once per item on arrival.
func BatchMeasure[T any](measureFn func(T) int, max int) StageOption
```

Both can be set simultaneously; the batch flushes on whichever trigger fires first.

**Usage:**
```go
// Count only
kitsune.Batch(p, kitsune.BatchCount(100))

// Measure only (e.g., byte budget)
kitsune.Batch(p, kitsune.BatchMeasure(func(b []byte) int { return len(b) }, 1<<20))

// Either trigger
kitsune.Batch(p, kitsune.BatchCount(100), kitsune.BatchMeasure(sizeOf, 1<<20))

// With timeout
kitsune.Batch(p, kitsune.BatchCount(100), kitsune.BatchTimeout(5*time.Second))
```

`BatchTimeout` already exists as a `StageOption` and is unchanged.

---

## 4. Signature Changes to Existing Operators

### 4.1 `RunningCountBy` / `RunningSumBy` — make key generic

Currently `CountBy` and `SumBy` in `compat.go` hardcode the key type as `string`. When renamed and moved to `aggregate.go`, the key type becomes generic:

```go
func RunningCountBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64]
func RunningSumBy[T any, K comparable, V Numeric](p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...StageOption) *Pipeline[map[K]V]
```

### 4.2 `TakeRandom` — terminal to buffering pipeline operator

`TakeRandom` currently has a terminal signature (`ctx` first, returns `([]T, error)`). It is reclassified as a buffering pipeline operator — it buffers all input using reservoir sampling, then emits a single `[]T` value when the source closes. Users call `Single` to collect the result.

```go
// Before
sample, err := kitsune.TakeRandom(ctx, p, 10)

// After
sample, err := kitsune.Single(ctx, kitsune.TakeRandom(p, 10))
```

New signature:
```go
func TakeRandom[T any](p *Pipeline[T], n int, opts ...StageOption) *Pipeline[[]T]
```

This aligns `TakeRandom` with the buffering model and makes it composable with further stages before collection.

---

## 5. Terminal API: `Collect` and `Single`

### `Collect` (already exists)

```go
func Collect[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) ([]T, error)
```

The safe, general-purpose terminal. Drains the pipeline and returns all emitted items as a slice. Already in `collect.go` — no changes needed, `ToSlice` never existed as a separate function.

### `Single` (new)

```go
func Single[T any](ctx context.Context, p *Pipeline[T], opts ...SingleOption) (T, error)
```

A strict terminal for pipelines expected to emit exactly one item (e.g., after `Reduce`, `Frequencies` as a barrier). Errors if the pipeline emits zero items (by default) or more than one item (always).

**Options:**

```go
// OrDefault returns v instead of an error when the pipeline emits no items.
func OrDefault[T any](v T) SingleOption

// OrZero returns the zero value of T instead of an error when the pipeline
// emits no items. Equivalent to OrDefault(zero).
func OrZero[T any]() SingleOption
```

**Usage:**
```go
freq, err  := Single(ctx, Frequencies(p))                   // error if empty
total, err := Single(ctx, Reduce(p, 0, add), OrZero[int]()) // 0 if empty stream
cfg, err   := Single(ctx, configs, OrDefault(defaultCfg))   // fallback if empty
```

**Guidance:** use `Single` when the pipeline is expected to produce exactly one value. Use `Collect` for multi-value pipelines. Calling `Collect` on a single-value pipeline is valid (returns a one-element slice); calling `Single` on a multi-value pipeline is a runtime error.

`First` and `Last` remain unchanged — they serve a different purpose (take the first/last of a potentially large stream, with short-circuit for `First`).

---

## 6. compat.go Dissolution

`compat.go` contains thin wrappers and renamed functions that were retained for backwards compatibility. All are removed or relocated in this audit.

| Symbol | Action |
|---|---|
| `ConsecutiveDedup` / `ConsecutiveDedupBy` | Remove. Use `Dedupe`/`DedupeBy` with `DedupeWindow(1)`. |
| `DeadLetter` / `DeadLetterSink` | Remove. Use `MapResult` directly. |
| `WindowByTime` | Remove. Has a bug: ignores `WithClock` and calls `time.NewTicker` directly. Replaced by `BufferWith(p, Ticker(d))`. |
| `MapBatch` | Move to `batch.go`. |
| `MapIntersperse` | Move to `misc.go`. |
| `EndWith` | Move to `misc.go` alongside `StartWith` (natural companions). |
| `Stage.Or` | Move to `pipeline.go` alongside the `Stage[I,O]` type definition. |
| `CountBy` | Rename to `RunningCountBy`, make key generic, move to `aggregate.go`. |
| `SumBy` | Rename to `RunningSumBy`, make key generic, move to `aggregate.go`. |

After these moves, `compat.go` is deleted.

---

## 7. New Operators

### `GroupBy` (redesigned)

The existing terminal `GroupBy(ctx, p, keyFn)` and `GroupByStream` are both removed and replaced by a single buffering pipeline operator:

```go
func GroupBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K][]T]
```

Buffers all input, groups items by key, and emits a single `map[K][]T` when the source closes. Use `Single` to collect the result, or pipe the map into further stages.

```go
// Collect the grouped map
groups, err := kitsune.Single(ctx, kitsune.GroupBy(orders, byRegion))

// Pipe into further processing
kitsune.Map(kitsune.GroupBy(orders, byRegion), summariseRegions)
```

**Category:** Buffering (emits one item on close).

---

### `InWindow`

```go
func InWindow[T, O any](p *Pipeline[[]T], stage func(*Pipeline[T]) *Pipeline[O], opts ...StageOption) *Pipeline[[]O]
```

Applies a pipeline transformation to the contents of each window. The `stage` parameter is a `Stage[T,O]` — it receives a mini-pipeline of the window's items and returns a transformed pipeline. All existing operators (`Sort`, `Filter`, `Map`, etc.) work inside `InWindow` without modification.

Use `Unbatch` to flatten the output back to `*Pipeline[O]` when the windowed structure is no longer needed.

```go
// Sort items within each chunk, then flatten back to a stream
result := kitsune.Unbatch(
    kitsune.InWindow(
        kitsune.Batch(p, kitsune.BatchCount(100)),
        func(w *Pipeline[int]) *Pipeline[int] {
            return kitsune.Sort(w, func(a, b int) bool { return a < b })
        },
    ),
)

// Filter within each session window
kitsune.InWindow(sessions, func(w *Pipeline[Event]) *Pipeline[Event] {
    return kitsune.Filter(w, isRelevant)
})
```

**Category:** Windowing (operates on `*Pipeline[[]T]`).

---

### `RandomSample`

```go
func RandomSample[T any](p *Pipeline[T], rate float64) *Pipeline[T]
```

A streaming (non-barrier) probabilistic filter. Each item passes independently with probability `rate` (0.0–1.0). Unlike `TakeRandom`, which buffers the entire stream for reservoir sampling, `RandomSample` processes items as they arrive and makes an independent decision per item.

**Category:** Filtering (streaming).

**Use case:** live stream sampling, A/B traffic splitting, telemetry downsampling.

```go
// Sample ~10% of events for a secondary analytics pipeline
arms := kitsune.Broadcast(events, 2)
full    := arms[0]
sampled := kitsune.RandomSample(arms[1], 0.10)
```

**Future work:** signal-driven sampling (a pipeline that provides the sampling decision per item, analogous to `BufferWith`'s relationship with `Ticker`) is a promising extension; deferred to a later design.

---

## 8. Removals

| Symbol | Reason |
|---|---|
| `BroadcastN` | Identical to `Broadcast`; `Broadcast` becomes the canonical implementation. |
| `Window` | Identical to `Batch`; already removed. |
| `ElementAt` | Niche; composable from `Drop(p, n)` + a single `ForEach`. Removing keeps the surface honest. |
| `Skip()` | Deprecated alias for `ActionDrop` in `config.go`; removes confusion with `DropUntil`/`DropLast` naming. |
| `Lift` | Deprecated alias for `LiftFallible` in `misc.go`; `LiftPure`/`LiftFallible` are already self-explanatory. |

---

## 9. Documentation Fixes

### Categorization corrections

| Operator | Current category | Correct category | Note |
|---|---|---|---|
| `Sort` / `SortBy` | Terminal | Transformation | Returns `*Pipeline[T]`; add "buffers all input" callout |
| `TakeRandom` | Terminal | Buffering pipeline operator | Signature changes (see §4.2); add "buffers all input — emits single `[]T` on close" callout |
| `ElementAt` | Filtering | — | Removed (see §8) |

### Aggregate section split

The aggregate section in `doc/operators.md` is split into two subsections:

**Running aggregates** (emit per item):
`RunningFrequencies`, `RunningFrequenciesBy`, `RunningCountBy`, `RunningSumBy`

**Buffering aggregates** (emit after source closes):
`GroupBy`, `Frequencies`, `FrequenciesBy`, `Reduce`

Note: `Scan` is a running aggregate (emits per item) and belongs in the first subsection.

### `BufferWith` / `Ticker` as `WindowByTime` replacement

The `WindowByTime` removal note in the changelog should point users to:

```go
// Replace WindowByTime(p, 5*time.Second) with:
kitsune.BufferWith(p, kitsune.Ticker(5*time.Second))
```

---

## Summary of All Changes

### Renames
- `SkipLast` → `DropLast`
- `SkipUntil` → `DropUntil`
- `WithLatestFrom` → `LatestFrom`
- `WithLatestFromWith` → `LatestFromWith`
- `FrequenciesStream` → `RunningFrequencies`
- `FrequenciesByStream` → `RunningFrequenciesBy`
- `CountBy` → `RunningCountBy`
- `SumBy` → `RunningSumBy`

### Removals
- `BroadcastN`
- `Window` (already done)
- `WindowByTime`
- `GroupByStream`
- `GroupBy(ctx, p, keyFn)` (terminal variant)
- `ConsecutiveDedup` / `ConsecutiveDedupBy`
- `DeadLetter` / `DeadLetterSink`
- `ElementAt`
- `Skip()` (deprecated `ErrorHandler` alias)
- `Lift` (deprecated `LiftFallible` alias)
- `compat.go` (file deleted after moves)

### Signature changes
- `Batch(p, size, opts...)` → `Batch(p, opts...)` with `BatchCount(n)` / `BatchMeasure(fn, max)`
- `TakeRandom(ctx, p, n, ...RunOption) ([]T, error)` → `TakeRandom(p, n, ...StageOption) *Pipeline[[]T]`
- `GroupBy` redesigned: terminal + `GroupByStream` → single buffering pipeline operator returning `*Pipeline[map[K][]T]`
- `Distinct`/`DistinctBy`: remove `WithDedupSet` code path
- `Dedupe`/`DedupeBy`: change default to global in-memory; add `DedupeWindow(n)`
- `RunningCountBy` / `RunningSumBy`: key type generalized from `string` to `K comparable`

### Additions
- `GroupBy[T, K](p, keyFn, ...StageOption) *Pipeline[map[K][]T]` (buffering pipeline operator)
- `InWindow[T, O](p *Pipeline[[]T], stage func(*Pipeline[T]) *Pipeline[O], ...StageOption) *Pipeline[[]O]`
- `Single[T](ctx, p, ...SingleOption) (T, error)` with `OrDefault` / `OrZero` options
- `RandomSample[T](p, rate float64) *Pipeline[T]`
- `BatchCount(n int) StageOption`
- `BatchMeasure[T](fn func(T) int, max int) StageOption`
- `DedupeWindow(n int) StageOption`

### File moves
- `MapBatch` → `batch.go`
- `MapIntersperse` → `misc.go`
- `EndWith` → `misc.go`
- `Stage.Or` → `pipeline.go`
- `RunningCountBy` / `RunningSumBy` → `aggregate.go`
