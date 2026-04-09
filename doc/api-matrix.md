# go-kitsune API Matrix: v1 → v2

Compares every exported operator between v1 (`archive/v1/`) and the current library.
Records name, signature changes, and which `StageOption` features each operator
actually uses in its implementation.

---

## Feature Key

| Column | Meaning |
|--------|---------|
| **Conc** | `Concurrency(n)` — parallel workers |
| **Ord** | `Ordered()` — preserve order in concurrent mode |
| **Buf** | `Buffer(n)` — output channel capacity |
| **Name** | `WithName(s)` — stage label for hooks/metrics |
| **Err** | `OnError(h)` — error handlers: `Halt`, `Skip`, `Return`, `Retry`, `RetryThen` |
| **Sup** | `Supervise(p)` — restart on error/panic: `RestartOnError`, `RestartOnPanic`, `RestartAlways` |
| **TO** | `Timeout(d)` — per-item deadline; cancels item context after d |
| **Cache** | `CacheBy(fn)` — memoize results; skip fn on cache hit |
| **OvF** | `Overflow(s)` — `Block` / `DropNewest` / `DropOldest` on full buffer |
| **Clock** | `WithClock(c)` — inject time source (for deterministic tests) |
| **DS** | `WithDedupSet(s)` — external deduplication backend |
| **BT** | `BatchTimeout(d)` — flush partial batch after d |
| **FP** | Fast-path / fusion — internal optimization for serial, hook-free chains |

### Cell values

| Symbol | Meaning |
|--------|---------|
| `●` | Supported in **both** v1 and v2 |
| `2` | **v2 only** (added or improved) |
| `1` | **v1 only** (not carried to v2) |
| `–` | Not applicable or not supported in either |
| `†` | v1 accepted option via `newNode` but the engine node kind may ignore it |
| `!` | Panics at construction time if used on this operator |

---

## Architecture Note

v1 routed most operators through a shared `newNode()` helper that blindly copied
every `stageConfig` field into an `engine.Node`. The engine then decided which
fields to honour per node kind. Operators that used `newNode` (Map, FlatMap,
SwitchMap, ExhaustMap, ForEach, and all state operators) technically *accepted*
every StageOption — but Timeout and CacheBy panicked at construction if used on
the wrong kind.

v2 is engineless: each operator owns its own goroutine loop and reads only the
`stageConfig` fields it actually needs. This makes feature support per-operator
and explicit, but means several operators lost options they had in v1 (notably
state operators lost Concurrency/OnError/Supervise; ForEach lost Concurrency).

---

## 1 · Core Transforms

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Map` | `Map` | identical | ● | ● | ● | ● | ● | ● | ● | ● | ● | – | – | – | 2 |
| `FlatMap` | `FlatMap` | identical | ● | ● | ● | ● | ● | ● | ● | – | ● | – | – | – | 2 |
| `ConcatMap` | `ConcatMap` | identical | – | – | ● | ● | ● | ● | ● | – | ● | – | – | – | – |
| `Filter` (method, `func(T)bool`) | `Filter` (free fn, `func(ctx,T)(bool,error)`) | pred signature changed; now free fn with opts | – | – | 2 | 2 | – | 2 | – | – | 2 | – | – | – | 2 |
| `(p).Tap(fn func(T))` | `Tap` (free fn, `func(ctx,T)error`) | fn signature changed; now free fn with opts | – | – | 2 | 2 | – | 2 | – | – | 2 | – | – | – | – |
| `Reject(pred func(T)bool)` | `Reject(pred func(ctx,T)(bool,error))` | pred signature changed | – | – | 2 | 2 | – | 2 | – | – | 2 | – | – | – | 2 |
| `(p).ForEach(fn, opts)` → `*Runner` | `(p).ForEach(fn, opts)` → `*ForEachRunner[T]` | return type changed | ● | ● | – | 2 | ● | ● | ● | – | – | – | – | – | 2 |
| `(p).Drain()` → `*Runner` | `(p).Drain()` → `*DrainRunner[T]` | return type changed; gains `Build()` | – | – | – | – | – | – | – | – | – | – | – | – | – |

**Notes**
- `Filter`, `Tap`, `Reject` had no StageOption support in v1; all feature marks in those rows are v2-only additions.
- `ForEach` in v1 supported Concurrency and OnError (both passed to engine). v2 restores Concurrency, Ordered, and OnError/Supervise as `StageOption`s — parity with v1 plus Ordered.
- `Drain` in v1 had no options. v2 adds `DrainRunner.Build()` for use with `MergeRunners`.
- All three of Map/FlatMap/Filter gain typed-fusion in v2: when the chain is serial, hook-free, and uses default overflow, stages fuse into a single goroutine with zero inter-stage channel hops (**FP** column).

---

## 2 · Higher-Order Maps

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `SwitchMap` | `SwitchMap` | identical | ● | – | ● | ● | ● | ● | ● | – | ● | – | – | – | – |
| `ExhaustMap` | `ExhaustMap` | identical | ● | – | ● | ● | ● | ● | ● | – | ● | – | – | – | – |
| `MapResult` | `MapResult` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `MapRecover` | `MapRecover` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `MapPooled` | `MapPooled` | `Pooled[T]` value type changed from value to pointer in v2 | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `MapBatch[I,O]` | `MapBatch[I,O]` (in compat.go) | identical | – | – | ● | ● | ● | – | – | – | – | – | – | ● | – |
| `MapEvery` | `MapEvery` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `MapIntersperse` | `MapIntersperse` (in compat.go) | gains opts | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| – | `DeadLetter` (in compat.go) | v2 only | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| – | `DeadLetterSink` (in compat.go) | v2 only | – | – | ● | ● | – | – | – | – | – | – | – | – | – |

**Notes**
- `SwitchMap`/`ExhaustMap` now support `Timeout` (per-item deadline on the inner fn context), `Supervise` (stage-level restart on error or panic), and `Overflow` (drop policy on the output channel).
- `MapPooled` in v2 takes `*Pool[O]` (unchanged) but the fn receives `*Pooled[O]` (pointer). `ReleaseAll` in v2 takes `[]*Pooled[T]` vs v1's `[]Pooled[T]`.
- `MapBatch` in compat.go delegates to `Batch` + `Map`; it therefore supports everything `Batch` supports for the batching step and `Map` for the processing step, but the compat wrapper itself only threads through `Buffer`, `Name`, `Err`, `BT`.

---

## 3 · State Transforms

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `MapWith` | `MapWith` | identical | ● | ● | ● | ● | ● | ● | ! | ! | ● | – | – | – | – |
| `FlatMapWith` | `FlatMapWith` | identical | ● | ● | ● | ● | ● | ● | ! | – | ● | – | – | – | – |
| `MapWithKey` | `MapWithKey` | identical | ● | ● | ● | ● | ● | ● | ! | ! | ● | – | – | – | – |
| `FlatMapWithKey` | `FlatMapWithKey` | identical | ● | ● | ● | ● | ● | ● | ! | – | ● | – | – | – | – |

**Notes**
- `!` in both v1 and v2 = Timeout/CacheBy panic at construction (not meaningful for stateful loops).
- **Concurrency semantics differ by operator type**:
  - `MapWith`/`FlatMapWith`: each of the n workers gets its own independent `Ref[S]` (worker-local state, no shared state). Use when parallelising expensive fns that benefit from per-worker accumulators.
  - `MapWithKey`/`FlatMapWithKey`: the key space is sharded across n workers via `hash(key) % n`. Each worker owns a disjoint partition — same-key items always reach the same worker, preserving per-entity state without any cross-worker locking (lock-free in the hot path).
- **Supervise** wraps the stage loop; the `Ref` (or `keyedRefMap`) is initialised outside the inner function and is preserved across restarts. Items consumed from the channel before the error are not re-delivered (Supervise is stage-level restart, not item-level retry; use `OnError(Retry(…))` for item-level retry).
- In v1 all four operators used `newNode(engine.Map/FlatMap, …)`, giving them Concurrency/OnError/Supervise nominally; the engine handled concurrency generically with a shared locked map for keyed state. v2's engineless per-operator concurrency enables the lock-free key-sharding design above.

---

## 4 · Batching & Windowing

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Batch` | `Batch` | identical | – | – | ● | ● | – | – | – | – | – | ● | – | ● | – |
| `Unbatch` (no opts) | `Unbatch` (gains opts) | v2 adds `...StageOption` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Window(d duration)` time-based | `Window(size int)` count-based | **parameter type changed** | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `Window(d duration)` | `WindowByTime(d duration)` (compat.go) | renamed for time-based | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| `SlidingWindow` (no opts) | `SlidingWindow` (gains opts) | v2 adds `...StageOption` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `SessionWindow` | `SessionWindow` | identical | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| – | `ChunkBy` | v2 only | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| – | `ChunkWhile` | v2 only | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |

**Notes**
- **Breaking change**: `Window` now groups by *count*, not *time*. Use `WindowByTime` (compat.go alias) to preserve v1 time-bucketing behaviour.
- `Batch` supports `WithClock` only when `BatchTimeout` is also set (the clock powers the flush ticker).
- `Window` (count-based) needs no clock; `WindowByTime`/`SessionWindow` require one for deterministic tests.
- `ChunkBy` groups consecutive items with the same key into one slice (emitted when the key changes). `ChunkWhile` groups while a predicate holds between adjacent items.

---

## 5 · Aggregation

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Scan` | `Scan` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `Reduce` (no opts) | `Reduce` (gains opts) | v2 adds `...StageOption` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Distinct` (no opts) | `Distinct` (gains opts) | v2 adds `...StageOption` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `DistinctBy(key func(T)string)` | `DistinctBy[T,K](keyFn func(T)K)` | key type generalised to `K comparable` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `(p).Dedupe(key func(T)string)` method | `Dedupe[T comparable]` free fn | key-less; comparable identity | – | – | ● | ● | – | – | – | – | – | – | ● | – | – |
| – | `DedupeBy[T,K]` free fn | v2 only (key-based equivalent) | – | – | 2 | 2 | – | – | – | – | – | – | 2 | – | – |
| `ConsecutiveDedup` | `ConsecutiveDedup` (compat.go) | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `ConsecutiveDedupBy` | `ConsecutiveDedupBy` (compat.go) | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `GroupBy` streaming `*Pipeline[map[K][]T]` | `GroupBy` terminal `(map[K][]T, error)` | **semantic change**: streaming → terminal | – | – | – | – | – | – | – | – | – | – | – | – | – |
| – | `GroupByStream` `*Pipeline[Group[K,T]]` | v2 only; streaming replacement | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `CountBy` | `CountBy` (compat.go) | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `SumBy` | `SumBy` (compat.go) | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| – | `FrequenciesStream` | v2 only | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| – | `FrequenciesByStream` | v2 only | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |

**Notes**
- **Semantic change**: `GroupBy` in v1 was a streaming mid-pipeline operator that emitted a single `map[K][]T` when the source closed (implemented as `Batch(MaxInt) + Map`). In v2 it is a terminal function. Use `GroupByStream` for mid-pipeline grouping.
- `GroupByStream` emits one `Group[K,T]{Key, Items}` per distinct key (in first-seen order) when the source closes.
- `Dedupe` in v2 is an identity-based dedup (`T comparable`); the v1 method that took a `key func(T)string` is preserved as `(p).Dedupe(keyFn)` in `methods.go` and delegates to `DedupeBy`.
- `FrequenciesStream`/`FrequenciesByStream` are new streaming accumulators that emit an updated count-map on each item.

---

## 6 · Fan-Out / Fan-In

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Broadcast(p, n)` (no opts) | `Broadcast(p, n, opts...)` | gains opts | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| – | `BroadcastN(p, n, opts...)` | v2 only (explicit N-way alias) | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Balance(p, n)` (no opts) | `Balance(p, n, opts...)` | gains opts | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Partition(p, fn func(T)bool)` | `Partition(p, fn func(T)bool, opts...)` | gains opts | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Merge` | `Merge` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `Zip` | `Zip` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `ZipWith` | `ZipWith` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `Unzip` (no opts) | `Unzip` (gains opts) | v2 adds `...StageOption` | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `CombineLatest` | `CombineLatest` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `CombineLatestWith` | `CombineLatestWith` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `WithLatestFrom` | `WithLatestFrom` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| – | `WithLatestFromWith` | v2 only | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Enrich` | `Enrich` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |
| `LookupBy` | `LookupBy` | identical | – | – | ● | ● | – | – | – | – | – | – | – | – | – |

**Notes**
- `Merge` and `Zip`/`CombineLatest`/`WithLatestFrom` (the plain pair versions) have no Buffer option because they create no buffered output channel beyond their inputs.
- The `*With` variants (ZipWith, CombineLatestWith, WithLatestFromWith) run a user fn so they do have a buffered output channel; Buffer and Name apply.
- `BroadcastN` is a v2-only alias for `Broadcast` with an explicit N (identical semantics; retained for clarity).

---

## 7 · Sources

Source operators produce items from external input and do not transform a pipeline.
None accept `StageOption` (they have no internal buffered channel to configure).

| v1 | v2 | Signature Δ |
|----|----|-|
| `From(ch <-chan T)` | `From(src <-chan T)` | param renamed |
| `FromSlice` | `FromSlice` | identical |
| `FromIter` | `FromIter` | identical |
| `Generate` | `Generate` | identical |
| `Unfold` | `Unfold` | identical |
| `Iterate` | `Iterate` | identical |
| `Repeatedly` | `Repeatedly` | identical |
| `Cycle` | `Cycle` | identical |
| `Concat` | `Concat` | identical |
| `Amb` | `Amb` | identical |
| `NewChannel[T]` | `NewChannel[T]` | identical |

**Time-based sources** — these accept `...StageOption` and use `Buffer`, `Name`, and `WithClock`:

| v1 | v2 | Signature Δ |
|----|----|-------------|
| `Ticker(d, opts...)` | `Ticker(d, opts...)` | identical |
| `Interval(d, opts...)` | `Interval(d, opts...)` | identical |
| `Timer(delay, fn, opts...)` | `Timer(delay, fn, opts...)` | identical |

---

## 8 · Terminal Functions

Terminal functions run the pipeline and return a materialised result.
They accept `...RunOption` (not `StageOption`). All behave identically in v1 and v2
except as noted.

| v1 | v2 | Signature Δ |
|----|----|-|
| `(p).Collect(ctx, opts...)` method | `Collect(ctx, p, opts...)` free fn + `(p).Collect` method | both forms available in v2 |
| `(p).First(ctx, opts...)` method | `First(ctx, p, opts...)` free fn + `(p).First` method | both forms |
| `(p).Last(ctx, opts...)` method | `Last(ctx, p, opts...)` free fn + `(p).Last` method | both forms |
| `(p).Count(ctx, opts...)` method | `Count(ctx, p, opts...)` free fn + `(p).Count` method | both forms |
| `(p).Any(ctx, fn, opts...)` method | `Any(ctx, p, fn, opts...)` free fn + `(p).Any` method | both forms |
| `(p).All(ctx, fn, opts...)` method | `All(ctx, p, fn, opts...)` free fn + `(p).All` method | both forms |
| `(p).ElementAt(ctx, i, opts...)` method | `ElementAt(ctx, p, i, opts...)` free fn + `(p).ElementAt` method | v1: method only; v2: both |
| `(p).Iter(ctx, opts...)` method | `Iter(ctx, p, opts...)` free fn + `(p).Iter` method | v1: method only; v2: both |
| `Find(ctx, p, pred, opts...)` | `Find(ctx, p, pred, opts...)` + `(p).Find` method | v2 adds method form |
| `Sum` | `Sum` | identical |
| `Min`, `Max`, `MinMax` | `Min`, `Max`, `MinMax` | identical |
| `MinBy`, `MaxBy` | `MinBy`, `MaxBy` | identical |
| `ReduceWhile` | `ReduceWhile` free fn + `(p).ReduceWhile` method | v2 method restricts S=T; free fn keeps `[T,S]` |
| `Contains` | `Contains` | identical |
| `ToMap` | `ToMap` | identical |
| `SequenceEqual` | `SequenceEqual` | identical |
| `TakeRandom` | `TakeRandom` | identical |
| `Frequencies` | `Frequencies` | identical |
| `FrequenciesBy` | `FrequenciesBy` | identical |
| `GroupBy` (streaming) | `GroupBy(ctx, p, keyFn, opts...)` terminal | semantic change — see §5 |
| – | `ErrEmpty` | v2 only: sentinel returned when no items produced |

---

## 9 · Timing & Observation

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Throttle(p, d, opts...)` | `Throttle(p, window, opts...)` | param renamed | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| `Debounce(p, d, opts...)` | `Debounce(p, silence, opts...)` | param renamed | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| `Timestamp` | `Timestamp` | identical | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| `TimeInterval` | `TimeInterval` | identical | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |

---

## 10 · Sequence Operators

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `(p).Take(n)` method | `Take(p, n)` free fn + `(p).Take` method | both forms in v2 | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `(p).Skip(n)` method | `Drop(p, n)` free fn + `(p).Skip` + `(p).Drop` methods | renamed; old method kept as alias | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `TakeWhile(pred func(T)bool)` | `TakeWhile(pred func(T)bool)` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DropWhile(pred func(T)bool)` | `DropWhile(pred func(T)bool)` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `TakeEvery(nth)` | `TakeEvery(n)` | param renamed | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DropEvery(nth)` | `DropEvery(n)` | param renamed | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `MapEvery(nth, fn)` no opts | `MapEvery(n, fn, opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `WithIndex` → `*Pipeline[Pair[int,T]]` no opts | `WithIndex(opts...)` → `*Pipeline[Indexed[T]]` | return type changed; gains opts | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Intersperse(sep)` no opts | `Intersperse(sep, opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `MapIntersperse(sep, fn)` no opts | `MapIntersperse(sep, fn, opts...)` gains opts | in compat.go | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `Pairwise` no opts | `Pairwise(opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `StartWith` | `StartWith` | identical | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DefaultIfEmpty` no opts | `DefaultIfEmpty(opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | 2 | – | – | – |
| `Sort` no opts | `Sort(opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |
| `SortBy` no opts | `SortBy(opts...)` gains opts | v2 adds StageOption | – | – | 2 | 2 | – | – | – | – | – | – | – | – | – |

**Notes**
- `Indexed[T]` (v2) is `struct{ Index int; Value T }`, replacing `Pair[int, T]` (v1).
- `Take`, `Drop`, `TakeWhile`, `DropWhile`, `TakeEvery`, `DropEvery` have no option support in either version; they use hardcoded buffer sizes.

---

## 11 · Middleware

| v1 | v2 | Signature Δ | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----|----|-------------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `RateLimit(p, rps, opts...)` | `RateLimit(p, rps, []RateLimitOpt, stageOpts...)` | RL-specific opts split into separate param | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |
| `CircuitBreaker(p, fn, opts...)` | `CircuitBreaker(p, fn, []CircuitBreakerOpt, stageOpts...)` | CB-specific opts split into separate param | – | – | ● | ● | – | – | – | – | – | ● | – | – | – |

**v1 StageOptions for RateLimit** (now `RateLimitOpt` in v2):

| v1 | v2 | Notes |
|----|----|-------|
| `Burst(n) StageOption` | `Burst(n) RateLimitOpt` | type changed |
| `RateMode(m) StageOption` | `RateMode(m) RateLimitOpt` | type changed |

**v1 StageOptions for CircuitBreaker** (now `CircuitBreakerOpt` in v2):

| v1 | v2 | Notes |
|----|----|-------|
| `FailureThreshold(n) StageOption` | `FailureThreshold(n) CircuitBreakerOpt` | type changed |
| `CooldownDuration(d) StageOption` | `CooldownDuration(d) CircuitBreakerOpt` | type changed |
| `HalfOpenProbes(n) StageOption` | `HalfOpenProbes(n) CircuitBreakerOpt` | type changed |
| `HalfOpenTimeout(d) StageOption` | `HalfOpenTimeout(d) CircuitBreakerOpt` | type changed |

**Notes**
- **Breaking change**: passing any of the four CB opts or either RL opt as `StageOption` to v2 silently does nothing (the underlying `stageConfig` field no longer exists). They must be passed in the dedicated `[]CircuitBreakerOpt` / `[]RateLimitOpt` parameter.
- Both operators still accept `WithName`, `Buffer`, and `WithClock` as `StageOption`.

---

## 12 · Stage Composition

| v1 | v2 | Signature Δ |
|----|----|-|
| `Stage[I,O]` type | `Stage[I,O]` type | identical |
| `(s Stage).Apply(p)` | `(s Stage).Apply(p)` | identical |
| `Then(s1, s2)` | `Then(s, next)` | param names only |
| `(p).Through(fn func(*Pipeline[T])*Pipeline[T])` | `(p).Through(s Stage[T,T])` | v2 takes a `Stage[T,T]`; since `Stage[T,T] = func(*Pipeline[T])*Pipeline[T]`, all existing code is compatible |
| `Or(primary, fallback func) Stage[I,O]` | `Or(primary, fallback func) Stage[I,O]` (stage.go) | restored in v2 |
| – | `(s Stage).Or(fallback Stage) Stage` method | v2 only; takes a full Stage, not raw fns |

---

## 13 · Error Routing

| v1 | v2 | Notes |
|----|----|-|
| `MapResult` | `MapResult` | identical; routes errors to a `*Pipeline[ErrItem[I]]` |
| `MapRecover` | `MapRecover` | identical; inline recovery fn |
| `DeadLetter` | `DeadLetter` (compat.go) | identical; alias for `MapResult` pattern |
| `DeadLetterSink` | `DeadLetterSink` (compat.go) | identical; sink variant |

---

## 14 · Helper / Lift Functions

| v1 | v2 | Notes |
|----|----|-|
| `Lift(fn func(I)(O,error))` | `Lift(fn)` | identical; adapts error-returning fn to `func(ctx,I)(O,error)` |
| `LiftPure(fn func(I)O)` | `LiftPure(fn)` | identical; wraps infallible fn |
| – | `LiftFallible(fn func(I)(O,error))` | v2 alias for `Lift` |
| – | `FilterFunc(fn func(T)bool)` | v2 helper; lifts plain pred for use with free-fn `Filter` |
| – | `RejectFunc(fn func(T)bool)` | v2 helper; lifts plain pred for use with free-fn `Reject` |
| – | `TapFunc(fn func(T))` | v2 helper; lifts void fn for use with free-fn `Tap` |

---

## 15 · Pool

| v1 | v2 | Signature Δ |
|----|----|-|
| `NewPool[T](factory func() T)` | `NewPool[T](newFn func() T)` | param renamed |
| `(p *Pool[T]).Warmup(n int)` | `(p *Pool[T]).Warmup(n int)` | identical |
| `MapPooled(p, pool, fn, opts...)` | `MapPooled(p, pool, fn, opts...)` | fn signature changed: v1 `fn(ctx, I, Pooled[T])`, v2 `fn(ctx, I, *Pooled[T])` |
| `ReleaseAll([]Pooled[T])` | `ReleaseAll([]*Pooled[T])` | slice element changed value→pointer |
| `(item Pooled[T]).Release()` | `(item *Pooled[T]).Release()` | receiver changed value→pointer |

---

## 16 · Observability

All identical between versions.

| Symbol | v1 | v2 |
|--------|----|----|
| `Hook` interface | `engine.Hook` | `internal.Hook` (re-exported) |
| `OverflowHook` | `engine.OverflowHook` | `internal.OverflowHook` |
| `SupervisionHook` | `engine.SupervisionHook` | `internal.SupervisionHook` |
| `SampleHook` | `engine.SampleHook` | `internal.SampleHook` |
| `GraphHook` | `engine.GraphHook` | `internal.GraphHook` |
| `BufferHook` | `engine.BufferHook` | `internal.BufferHook` |
| `MetricsHook` | `Snapshot` | `MetricsSnapshot` (renamed) |
| `LogHook` | ● | ● |
| `MultiHook` | ● | ● |
| `JSONCodec` | – | `JSONCodec` (v2 only; explicit alias) |

---

## Summary: Features Gained and Lost in v2

### Gained in v2

| Feature | Details |
|---------|---------|
| Fast path / fusion | Serial Map → FlatMap → ForEach chains fuse into one goroutine with zero channel hops |
| `Filter`/`Tap`/`Reject` supervision | Stages can now restart on error/panic via `Supervise` |
| `Filter`/`Tap`/`Reject` overflow | Output buffer drop policy configurable via `Overflow` |
| `ForEach` Concurrency/Ordered/OnError/Supervise restored | v2 restores `Concurrency(n)`, `Ordered()`, `OnError`, and `Supervise` on `ForEach` — parity with v1 plus the new `Ordered()` mode |
| `SwitchMap`/`ExhaustMap` Timeout/Supervise/Overflow | Timeout threads a per-item deadline into the inner fn context; Supervise enables stage-level restart; Overflow controls the output channel drop policy |
| `Or` as Stage method | `stage1.Or(stage2)` chains two full Stages as try/fallback |
| `BloomDedupSet` | Probabilistic dedup with bounded memory |
| `DedupeBy` | Key-based dedup as a free function (was method-only in v1) |
| `GroupByStream` | Streaming GroupBy that emits `Group[K,T]` mid-pipeline |
| `FrequenciesStream`/`FrequenciesByStream` | Streaming count accumulation |
| `WithLatestFromWith` | Combining variant of `WithLatestFrom` |
| `BroadcastN` | Explicit N-way broadcast alias |
| `ChunkBy`/`ChunkWhile` | Key-change and predicate-change grouping |
| `ErrEmpty` | Sentinel error for "no items produced" |
| `LiftFallible`, `FilterFunc`, `RejectFunc`, `TapFunc` | Adapter helpers for plain functions |
| `DrainRunner.Build()` | Drain branches can now be used with `MergeRunners` |
| `Iter`/`ElementAt` as Pipeline methods | Both forms available |
| `MapWithKey`/`FlatMapWithKey` key-sharded concurrency | Lock-free per-entity state: key space sharded across n workers by `hash(key) % n`; same-key items always reach the same worker; no cross-worker locking in the hot path |
| `MapWith`/`FlatMapWith` worker-local concurrency | Each of the n workers owns an independent `Ref[S]`; useful for parallelising expensive fns needing per-worker state |
| OnError/Supervise on all state operators | All four operators now read `cfg.errorHandler` and wrap with `internal.Supervise`; Ref values are preserved across restarts |

### Lost or Regressed in v2

| Feature | Details |
|---------|---------|
| State operators (MapWith etc.) Concurrency/Ordered/OnError/Supervise | **Restored in v2**: all four operators now support Concurrency/Ordered/OnError/Supervise. Concurrency semantics are explicitly defined per operator (worker-local for MapWith; key-sharded for MapWithKey). |
| `ErrGraphMismatch` | Removed (architecture changed; runners no longer hold a shared graph pointer) |
| `Snapshot` type | Renamed to `MetricsSnapshot` |
| CB/RL options as `StageOption` | `FailureThreshold`, `CooldownDuration`, `HalfOpenProbes`, `HalfOpenTimeout`, `Burst`, `RateMode` are now dedicated opt types — passing them as StageOption silently does nothing |
