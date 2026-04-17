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
| **Buffering** | Must consume all input before emitting; returns `*Pipeline[T]` | `Sort`, `TakeRandom`, `GroupByStream` |
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

`GroupByStream` is **not** renamed. It is a buffering operator (emits after source closes, not per item) so `Running*` would be wrong. It also cannot take the name `GroupBy` because a terminal `GroupBy` already exists. The `Stream` suffix here means "pipeline-returning version," which is accurate and distinct from the `Running*` convention.

### 1.2 `Drop*` prefix consistency

`Drop`, `DropWhile`, `DropEvery` are established. Two stragglers are aligned:

| Current | New |
|---|---|
| `SkipLast` | `DropLast` |
| `SkipUntil` | `DropUntil` |

`Skip()` in `config.go` is an `ErrorHandler`, not an operator — leave it.

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

## 4. Terminal API: `Collect` and `Single`

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

## 5. compat.go Dissolution

`compat.go` contains thin wrappers and renamed functions that were retained for backwards compatibility. All are removed or relocated in this audit.

| Symbol | Action |
|---|---|
| `ConsecutiveDedup` / `ConsecutiveDedupBy` | Remove. Use `Dedupe`/`DedupeBy` with `DedupeWindow(1)`. |
| `DeadLetter` / `DeadLetterSink` | Remove. Use `MapResult` directly. |
| `WindowByTime` | Remove. Has a bug: ignores `WithClock` and calls `time.NewTicker` directly. Replaced by `BufferWith(p, Ticker(d))`. |
| `MapBatch` | Move to `batch.go`. |
| `MapIntersperse` | Move to `misc.go`. |
| `CountBy` | Rename to `RunningCountBy`, move to `aggregate.go`. |
| `SumBy` | Rename to `RunningSumBy`, move to `aggregate.go`. |

After these moves, `compat.go` is deleted.

---

## 6. New Operators

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

## 7. Removals

| Symbol | Reason |
|---|---|
| `BroadcastN` | Identical to `Broadcast`; `Broadcast` becomes the canonical implementation. |
| `Window` | Identical to `Batch`; already removed. |
| `ElementAt` | Niche; composable from `Drop(p, n)` + a single `ForEach`. Removing keeps the surface honest. |

---

## 8. Documentation Fixes

### Categorization corrections

| Operator | Current category | Correct category | Note |
|---|---|---|---|
| `Sort` / `SortBy` | Terminal | Transformation | Returns `*Pipeline[T]`; add "buffers all input" callout |
| `TakeRandom` | Filtering | Transformation | Returns `([]T, error)`; add "buffers all input" callout |
| `GroupByStream` | (unlabeled) | Transformation | Add "buffers all input — emits one Group per key on close" callout |
| `ElementAt` | Filtering | — | Removed (see §7) |

### Aggregate section split

The aggregate section in `doc/operators.md` is split into two subsections:

**Running aggregates** (emit per item):
`RunningFrequencies`, `RunningFrequenciesBy`, `RunningCountBy`, `RunningSumBy`

**Barrier aggregates** (emit after source closes):
`GroupByStream`, `Frequencies`, `FrequenciesBy`, `GroupBy` (terminal), `Reduce`, `Scan`

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
- `ConsecutiveDedup` / `ConsecutiveDedupBy`
- `DeadLetter` / `DeadLetterSink`
- `ElementAt`
- `compat.go` (file deleted after moves)

### Signature changes
- `Batch(p, size, opts...)` → `Batch(p, opts...)` with `BatchCount(n)` / `BatchMeasure(fn, max)`
- `Distinct`/`DistinctBy`: remove `WithDedupSet` code path
- `Dedupe`/`DedupeBy`: change default to global in-memory; add `DedupeWindow(n)`

### Additions
- `Single[T](ctx, p, ...SingleOption) (T, error)` with `OrDefault` / `OrZero` options
- `RandomSample[T](p, rate float64) *Pipeline[T]`
- `BatchCount(n int) StageOption`
- `BatchMeasure[T](fn func(T) int, max int) StageOption`
- `DedupeWindow(n int) StageOption`

### File moves
- `MapBatch` → `batch.go`
- `MapIntersperse` → `misc.go`
- `RunningCountBy` / `RunningSumBy` → `aggregate.go`
