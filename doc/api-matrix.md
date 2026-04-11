# go-kitsune API Reference

Documents every exported operator and which `StageOption` features each one actually uses in its implementation.

---

## Feature Key

| Column | Meaning |
|--------|---------|
| **Conc** | `Concurrency(n)`: parallel workers |
| **Ord** | `Ordered()`: preserve order in concurrent mode |
| **Buf** | `Buffer(n)`: output channel capacity |
| **Name** | `WithName(s)`: stage label for hooks/metrics |
| **Err** | `OnError(h)`: error handlers: `Halt`, `Skip`, `Return`, `Retry`, `RetryThen` |
| **Sup** | `Supervise(p)`: restart on error/panic: `RestartOnError`, `RestartOnPanic`, `RestartAlways` |
| **TO** | `Timeout(d)`: per-item deadline; cancels item context after d |
| **Cache** | `CacheBy(fn)`: memoize results; skip fn on cache hit |
| **OvF** | `Overflow(s)`: `Block` / `DropNewest` / `DropOldest` on full buffer |
| **Clock** | `WithClock(c)`: inject time source (for deterministic tests) |
| **DS** | `WithDedupSet(s)`: external deduplication backend |
| **BT** | `BatchTimeout(d)`: flush partial batch after d |
| **FP** | Fast-path / fusion: internal optimization for serial, hook-free chains |

### Cell values

| Symbol | Meaning |
|--------|---------|
| `✓` | Supported |
| `–` | Not applicable or not supported |
| `!` | Panics at construction time if used on this operator |

---

## 1 · Core Transforms

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Map` | `Map[I,O](p, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – | – | – | ✓ |
| `FlatMap` | `FlatMap[I,O](p, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – | ✓ | – | – | – | ✓ |
| `ConcatMap` | `ConcatMap[I,O](p, fn, opts...)` | – | – | ✓ | ✓ | ✓ | ✓ | ✓ | – | ✓ | – | – | – | – |
| `Filter` | `Filter[T](p, pred func(ctx,T)(bool,error), opts...)` | – | – | ✓ | ✓ | – | ✓ | – | – | ✓ | – | – | – | ✓ |
| `Tap` | `Tap[T](p, fn func(ctx,T)error, opts...)` | – | – | ✓ | ✓ | – | ✓ | – | – | ✓ | – | – | – | – |
| `TapError` | `TapError[T](p, fn func(ctx,error))` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `Finally` | `Finally[T](p, fn func(ctx,error))` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `ExpandMap` | `ExpandMap[T](p, fn func(ctx,T)*Pipeline[T], opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | ✓ | – | – |
| `Reject` | `Reject[T](p, pred func(ctx,T)(bool,error), opts...)` | – | – | ✓ | ✓ | – | ✓ | – | – | ✓ | – | – | – | ✓ |
| `IgnoreElements` | `IgnoreElements[T](p)`; also `(p).IgnoreElements()` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `ForEach` | `(p).ForEach(fn, opts...)` → `*ForEachRunner[T]` | ✓ | ✓ | – | ✓ | ✓ | ✓ | ✓ | – | – | – | – | – | ✓ |
| `Drain` | `(p).Drain()` → `*DrainRunner[T]` | – | – | – | – | – | – | – | – | – | – | – | – | – |

**Notes**
- `Filter`, `Tap`, `Reject` support `Supervise` but not `OnError`; errors from their fn/pred propagate directly.
- `TapError` fires its callback only for non-context errors; context cancellation does not trigger the callback. It does not accept `StageOption` (implemented via `Generate`, like `Catch`).
- `Finally` fires for all exits (success, error, cancellation, early consumer stop). On early stop (e.g. downstream `Take`), fn receives nil. Does not accept `StageOption`.
- `ExpandMap` performs BFS expansion: items at depth N are all emitted before any item at depth N+1. fn may return nil for leaf nodes. Accepts `WithName` and `Buffer`. Use `VisitedBy(keyFn)` to enable cycle detection (items whose key was already seen are skipped, along with their subtrees); combine with `WithDedupSet` to override the default `MemoryDedupSet` backend.
- `ForEach` returns a typed `ForEachRunner[T]`; call `.Run(ctx)` or `.RunAsync(ctx)`. Supports `Concurrency`, `Ordered`, `OnError`, and `Supervise`.
- `Drain` returns a `DrainRunner[T]` with a `Build()` method for use with `MergeRunners`.
- Map → FlatMap → ForEach chains fuse into a single goroutine when the chain is serial, hook-free, and uses default overflow (**FP** column).

---

## 2 · Higher-Order Maps

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `SwitchMap` | `SwitchMap[I,O](p, fn, opts...)` | ✓ | – | ✓ | ✓ | ✓ | ✓ | ✓ | – | ✓ | – | – | – | – |
| `ExhaustMap` | `ExhaustMap[I,O](p, fn, opts...)` | ✓ | – | ✓ | ✓ | ✓ | ✓ | ✓ | – | ✓ | – | – | – | – |
| `MapResult` | `MapResult[I,O](p, fn, opts...)` → `(*Pipeline[O], *Pipeline[ErrItem[I]])` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `MapRecover` | `MapRecover[I,O](p, fn, recover, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `MapPooled` | `MapPooled[I,O](p, pool, fn func(ctx,I,*Pooled[O])error, opts...)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `MapBatch` *(compat)* | `MapBatch[I,O](p, size, fn, opts...)` | – | – | ✓ | ✓ | ✓ | – | – | – | – | – | – | ✓ | – |
| `MapEvery` | `MapEvery[I,O](p, n, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `MapIntersperse` *(compat)* | `MapIntersperse[T,O](p, sep, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `DeadLetter` *(compat)* | `DeadLetter[I,O](p, fn, opts...)` → `(*Pipeline[O], *Pipeline[ErrItem[I]])` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `DeadLetterSink` *(compat)* | `DeadLetterSink[I](p, fn, opts...)` → `(*Pipeline[ErrItem[I]], *Runner)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |

**Notes**
- `SwitchMap` cancels the active inner pipeline when a new upstream item arrives. `ExhaustMap` ignores new items while an inner pipeline is active.
- `Timeout` on `SwitchMap`/`ExhaustMap` threads a per-item deadline into the inner fn context.
- `MapBatch` (compat) delegates to `Batch` + `FlatMap`; it only threads `Buffer`, `Name`, `Err`, and `BatchTimeout` to the internal `Batch` stage.
- `MapPooled`: fn receives `*Pooled[O]` (pointer). `ReleaseAll` takes `[]*Pooled[T]`. `Pooled[T].Release()` has a pointer receiver.
- `MapResult` and `DeadLetter` both branches must be consumed before calling `Run`.

---

## 3 · State Transforms

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `MapWith` | `MapWith[I,O,S](p, key, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ! | ! | ✓ | – | – | – | – |
| `FlatMapWith` | `FlatMapWith[I,O,S](p, key, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ! | – | ✓ | – | – | – | – |
| `MapWithKey` | `MapWithKey[I,O,S](p, key, itemKeyFn, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ! | ! | ✓ | – | – | – | – |
| `FlatMapWithKey` | `FlatMapWithKey[I,O,S](p, key, itemKeyFn, fn, opts...)` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ! | – | ✓ | – | – | – | – |

**Notes**
- `!`: `Timeout` and `CacheBy` panic at construction; they are not meaningful for stateful loops.
- **Concurrency semantics differ by operator**:
  - `MapWith` / `FlatMapWith`: each of n workers gets its own independent `Ref[S]` (worker-local state).
  - `MapWithKey` / `FlatMapWithKey`: the key space is sharded across n workers via `hash(key) % n`. Same-key items always reach the same worker; lock-free in the hot path.
- `Supervise` wraps the stage loop; the `Ref` (or keyed ref map) is initialised outside the inner fn and is preserved across restarts.
- State TTL: `NewKey("name", initial, StateTTL(d))`. `Ref.Get` returns the zero value and resets the slot when the TTL has elapsed.

---

## 4 · Batching & Windowing

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Batch` | `Batch[T](p, size, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | ✓ | – |
| `BufferWith` | `BufferWith[T,S](p, closingSelector, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Unbatch` | `Unbatch[T](p, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Window` | `Window[T](p, size int, opts...)`: count-based | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `WindowByTime` *(compat)* | `WindowByTime[T](p, d, opts...)`: time-based | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `SlidingWindow` | `SlidingWindow[T](p, size, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `SessionWindow` | `SessionWindow[T](p, gap, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `ChunkBy` | `ChunkBy[T,K](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `ChunkWhile` | `ChunkWhile[T](p, pred func(prev,curr T)bool, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |

**Notes**
- `Window` groups by *count*. Use `WindowByTime` (compat alias) for time-bucketing.
- `Batch` supports `WithClock` only when `BatchTimeout` is also set (the clock powers the flush ticker).
- `BufferWith` takes a second pipeline (`closingSelector`) as its flush trigger; each signal from that pipeline emits the current buffer. When the selector closes, any remaining items are flushed. Named `BufferWith` to avoid collision with the `Buffer(n)` stage option.
- `ChunkBy` emits a group when the key changes. `ChunkWhile` emits a group when the predicate between adjacent items is false.

---

## 5 · Aggregation

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Scan` | `Scan[T,S](p, seed, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Reduce` | `Reduce[T,S](p, seed, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Distinct` | `Distinct[T comparable](p, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `DistinctBy` | `DistinctBy[T,K comparable](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Dedupe` | `Dedupe[T comparable](p, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | ✓ | – | – |
| `DedupeBy` | `DedupeBy[T,K comparable](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | ✓ | – | – |
| `ConsecutiveDedup` *(compat)* | `ConsecutiveDedup[T comparable](p, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `ConsecutiveDedupBy` *(compat)* | `ConsecutiveDedupBy[T,K comparable](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `GroupBy` | `GroupBy[T,K](ctx, p, keyFn, opts...)` → `(map[K][]T, error)`: terminal | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `GroupByStream` | `GroupByStream[T,K](p, keyFn, opts...)` → `*Pipeline[Group[K,T]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `CountBy` *(compat)* | `CountBy[T,K](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `SumBy` *(compat)* | `SumBy[T,K,V](p, keyFn, valFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `FrequenciesStream` | `FrequenciesStream[T comparable](p, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `FrequenciesByStream` | `FrequenciesByStream[T,K comparable](p, keyFn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |

**Notes**
- `GroupBy` is a terminal function returning `(map[K][]T, error)`. Use `GroupByStream` for mid-pipeline grouping.
- `GroupByStream` emits one `Group[K,T]{Key, Items}` per distinct key in first-seen order when the source closes.
- `Dedupe` is identity-based (`T comparable`). `DedupeBy` is key-based. When `WithDedupSet` is provided to either, deduplication becomes global (set-backed) rather than consecutive.
- `FrequenciesStream` / `FrequenciesByStream` emit an updated count-map after each item.
- `CountBy` / `SumBy` run at `Concurrency(1)` and emit a full snapshot after each item.

---

## 6 · Fan-Out / Fan-In

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Broadcast` | `Broadcast[T](p, n, opts...)` → `[]*Pipeline[T]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `BroadcastN` | `BroadcastN[T](p, n, opts...)` → `[]*Pipeline[T]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Balance` | `Balance[T](p, n, opts...)` → `[]*Pipeline[T]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `KeyedBalance` | `KeyedBalance[T](p, n, keyFn, opts...)` → `[]*Pipeline[T]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Share` | `Share[T](p, opts...)` → `func(opts...) *Pipeline[T]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Partition` | `Partition[T](p, pred, opts...)` → `(*Pipeline[T], *Pipeline[T])` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Merge` | `Merge[T](pipelines...)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `Zip` | `Zip[A,B](a, b)` → `*Pipeline[Pair[A,B]]` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `ZipWith` | `ZipWith[A,B,O](a, b, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Unzip` | `Unzip[A,B](p, opts...)` → `(*Pipeline[A], *Pipeline[B])` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `CombineLatest` | `CombineLatest[A,B](a, b)` → `*Pipeline[Pair[A,B]]` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `CombineLatestWith` | `CombineLatestWith[A,B,O](a, b, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `WithLatestFrom` | `WithLatestFrom[T,U](p, other)` → `*Pipeline[Pair[T,U]]` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `WithLatestFromWith` | `WithLatestFromWith[T,U,O](p, other, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Enrich` | `Enrich[T,K,V,O](p, keyFn, fetch, join, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `LookupBy` | `LookupBy[T,K,V](p, keyFn, fetch, opts...)` → `*Pipeline[Pair[T,V]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |

**Notes**
- `Merge`, `Zip`, `CombineLatest`, `WithLatestFrom` create no buffered output channel of their own, so `Buffer` does not apply.
- The `*With` variants run a user fn and do produce a buffered output channel; `Buffer` and `Name` apply.
- `BroadcastN` is an explicit N-way alias for `Broadcast` (identical semantics).
- `Broadcast` requires `n ≥ 2`.
- `Share` returns a factory; call the factory once per desired branch before building the runner. At least one subscribe call is required. `Buffer` and `WithName` can be set on each individual subscribe call (per-subscribe opts override factory opts). Calling the factory after `Run()` has started panics. Unlike `Broadcast`, `Share` allows a single subscriber.

---

## 7 · Sources

Source operators produce items from external input. They accept no `StageOption`.

| Operator | Signature |
|----------|-----------|
| `From` | `From[T](src <-chan T)` |
| `FromSlice` | `FromSlice[T](items []T)` |
| `FromIter` | `FromIter[T](iter func(yield func(T)bool))` |
| `Generate` | `Generate[T](fn func(ctx)(T,error))` |
| `Unfold` | `Unfold[S,T](seed S, fn func(ctx,S)(T,S,bool,error))` |
| `Iterate` | `Iterate[T](seed T, fn func(T)T)` |
| `Repeatedly` | `Repeatedly[T](fn func()T)` |
| `Cycle` | `Cycle[T](items []T)` |
| `Empty` | `Empty[T]()` |
| `Never` | `Never[T]()` |
| `Concat` | `Concat[T](factories ...func()*Pipeline[T])` |
| `Amb` | `Amb[T](factories ...func()*Pipeline[T])` |
| `Using` | `Using[T,R](acquire func(ctx)(R,error), build func(R)*Pipeline[T], release func(R))` |
| `NewChannel` | `NewChannel[T]()` → `*Channel[T]` (with `Send`, `TrySend`, `Close`) |

**Time-based sources**: accept `Buffer`, `Name`, and `WithClock` as `StageOption`:

| Operator | Signature |
|----------|-----------|
| `Ticker` | `Ticker(d, opts...)` → `*Pipeline[time.Time]` |
| `Timer` | `Timer(delay, fn, opts...)` → `*Pipeline[T]` |

---

## 8 · Terminal Functions

Terminal functions run the pipeline and return a materialised result. They accept `...RunOption` (not `StageOption`).

| Operator | Signature |
|----------|-----------|
| `Collect` | `Collect[T](ctx, p, opts...)` → `([]T, error)`; also `(p).Collect` |
| `First` | `First[T](ctx, p, opts...)` → `(T, bool, error)`; also `(p).First` |
| `Last` | `Last[T](ctx, p, opts...)` → `(T, bool, error)`; also `(p).Last` |
| `Count` | `Count[T](ctx, p, opts...)` → `(int, error)`; also `(p).Count` |
| `Any` | `Any[T](ctx, p, fn, opts...)` → `(bool, error)`; also `(p).Any` |
| `All` | `All[T](ctx, p, fn, opts...)` → `(bool, error)`; also `(p).All` |
| `Find` | `Find[T](ctx, p, pred, opts...)` → `(T, bool, error)`; also `(p).Find` |
| `ElementAt` | `ElementAt[T](ctx, p, i, opts...)` → `(T, bool, error)`; also `(p).ElementAt` |
| `Iter` | `Iter[T](ctx, p, opts...)` → `(iter.Seq[T], func()error)`; also `(p).Iter` |
| `ReduceWhile` | `ReduceWhile[T,S](ctx, p, seed, fn, opts...)` → `(S, error)`; also `(p).ReduceWhile` (S=T) |
| `GroupBy` | `GroupBy[T,K](ctx, p, keyFn, opts...)` → `(map[K][]T, error)` |
| `Sum` | `Sum[T](ctx, p, opts...)` → `(T, error)` |
| `Min` / `Max` | `Min[T](ctx, p, opts...)` → `(T, bool, error)` |
| `MinMax` | `MinMax[T](ctx, p, opts...)` → `(Pair[T,T], bool, error)` |
| `MinBy` / `MaxBy` | `MinBy[T,K](ctx, p, keyFn, less, opts...)` → `(T, bool, error)` |
| `Contains` | `Contains[T comparable](ctx, p, val, opts...)` → `(bool, error)` |
| `ToMap` | `ToMap[T,K,V](ctx, p, keyFn, valFn, opts...)` → `(map[K]V, error)` |
| `SequenceEqual` | `SequenceEqual[T comparable](ctx, a, b, opts...)` → `(bool, error)` |
| `TakeRandom` | `TakeRandom[T](ctx, p, n, opts...)` → `([]T, error)` |
| `Frequencies` | `Frequencies[T comparable](ctx, p, opts...)` → `(map[T]int, error)` |
| `FrequenciesBy` | `FrequenciesBy[T,K comparable](ctx, p, keyFn, opts...)` → `(map[K]int, error)` |

**Notes**
- `ErrEmpty` is a sentinel returned by `First`, `Last`, `ElementAt`, and similar when the stream produces no items.
- `Iter` exposes a pipeline as `iter.Seq[T]` for range-over-func (Go 1.23+). Breaking out of the loop early cancels the pipeline.

---

## 9 · Timing & Observation

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Throttle` | `Throttle[T](p, window, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `Debounce` | `Debounce[T](p, silence, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `Sample` | `Sample[T](p, d, opts...)`: emit latest item per tick | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `Timestamp` | `Timestamp[T](p, opts...)` → `*Pipeline[Timestamped[T]]` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `TimeInterval` | `TimeInterval[T](p, opts...)` → `*Pipeline[TimedInterval[T]]` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |

**Notes**
- `Sample` emits the most-recently-seen item on each tick and resets the latch; ticks with no new item produce no output. Unlike `Debounce`, it does **not** flush on source close.

---

## 10 · Sequence Operators

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `Take` | `Take[T](p, n)`; also `(p).Take` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `Drop` | `Drop[T](p, n)`; also `(p).Skip`, `(p).Drop` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `TakeWhile` | `TakeWhile[T](p, pred func(T)bool)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DropWhile` | `DropWhile[T](p, pred func(T)bool)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `SkipLast` | `SkipLast[T](p, n)`: omit last n items | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `TakeEvery` | `TakeEvery[T](p, n)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DropEvery` | `DropEvery[T](p, n)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `MapEvery` | `MapEvery[I,O](p, n, fn, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `WithIndex` | `WithIndex[T](p, opts...)` → `*Pipeline[Indexed[T]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Intersperse` | `Intersperse[T](p, sep, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `Pairwise` | `Pairwise[T](p, opts...)` → `*Pipeline[Pair[T,T]]` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `TakeUntil` | `TakeUntil[T,U](p, boundary *Pipeline[U], opts...)`: pass items until boundary emits | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `SkipUntil` | `SkipUntil[T,U](p, boundary *Pipeline[U], opts...)`: skip items until boundary emits | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `StartWith` | `StartWith[T](p, items...)` | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `EndWith` | `EndWith[T](p, items...)`: append fixed items after source closes | – | – | – | – | – | – | – | – | – | – | – | – | – |
| `DefaultIfEmpty` | `DefaultIfEmpty[T](p, val, opts...)` | – | – | ✓ | ✓ | – | – | – | – | ✓ | – | – | – | – |
| `Sort` | `Sort[T](p, less, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |
| `SortBy` | `SortBy[T,K](p, keyFn, less, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | – | – | – | – |

**Notes**
- `Take`, `Drop`, `TakeWhile`, `DropWhile`, `TakeEvery`, `DropEvery`, `SkipLast` use hardcoded buffer sizes and accept no options.
- `TakeUntil` / `SkipUntil` accept any `*Pipeline[U]` as boundary; only its first emission matters.
- `StartWith` / `EndWith` accept no options; they delegate to `Concat` + `FromSlice`.
- `Indexed[T]` is `struct{ Index int; Value T }`.
- `(p).Skip` is an alias for `Drop`.

---

## 11 · Middleware

| Operator | Signature | Conc | Ord | Buf | Name | Err | Sup | TO | Cache | OvF | Clock | DS | BT | FP |
|----------|-----------|------|-----|-----|------|-----|-----|----|-------|-----|-------|----|----|-----|
| `RateLimit` | `RateLimit[T](p, rps float64, rlOpts []RateLimitOpt, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |
| `CircuitBreaker` | `CircuitBreaker[T](p, fn, cbOpts []CircuitBreakerOpt, opts...)` | – | – | ✓ | ✓ | – | – | – | – | – | ✓ | – | – | – |

**`RateLimitOpt`** (not `StageOption`):

| Option | Effect |
|--------|--------|
| `Burst(n int)` | Token bucket burst size |
| `RateMode(m)` | `RateLimitWait` (backpressure) or `RateLimitDrop` (skip excess) |

**`CircuitBreakerOpt`** (not `StageOption`):

| Option | Effect |
|--------|--------|
| `FailureThreshold(n int)` | Failures before opening |
| `CooldownDuration(d)` | Time in open state before half-open |
| `HalfOpenProbes(n int)` | Successes required to close from half-open |
| `HalfOpenTimeout(d)` | If probes not received within d, reopen |

**Notes**
- `WithName`, `Buffer`, and `WithClock` are the only `StageOption`s that apply to both operators.
- `CircuitBreaker` emits `ErrCircuitOpen` when the circuit is open.

---

## 12 · Stage Composition

| Symbol | Signature | Notes |
|--------|-----------|-------|
| `Stage[I,O]` | `type Stage[I,O any] func(*Pipeline[I]) *Pipeline[O]` | Named function type; zero-cost transformer |
| `(s).Apply` | `(s Stage[I,O]).Apply(p *Pipeline[I]) *Pipeline[O]` | Apply a stage to a pipeline |
| `Then` | `Then[I,M,O](s1 Stage[I,M], s2 Stage[M,O]) Stage[I,O]` | Compose two stages |
| `(p).Through` | `(p *Pipeline[T]).Through(s Stage[T,T]) *Pipeline[T]` | Method form of Apply |
| `Or` | `Or[I,O](primary, fallback func) Stage[I,O]` | Try primary; fall back to fallback on no output |
| `(s).Or` | `(s Stage[I,O]).Or(fallback Stage[I,O]) Stage[I,O]` *(compat)* | Method form taking a full Stage |

---

## 13 · Error Routing

| Operator | Signature | Notes |
|----------|-----------|-------|
| `Catch` | `Catch[T](p, fn func(error)*Pipeline[T])` | On pipeline error, switch to fallback pipeline returned by fn |
| `MapResult` | `MapResult[I,O](p, fn, opts...)` → `(*Pipeline[O], *Pipeline[ErrItem[I]])` | Routes errors to a dead-letter branch |
| `MapRecover` | `MapRecover[I,O](p, fn, recover, opts...)` | Inline recovery fn produces a fallback value |
| `DeadLetter` *(compat)* | `DeadLetter[I,O](p, fn, opts...)` | `MapResult` with retry wrapping |
| `DeadLetterSink` *(compat)* | `DeadLetterSink[I](p, fn, opts...)` | Sink variant; returns dead-letter pipeline + runner |

---

## 14 · Helper / Lift Functions

| Function | Signature | Notes |
|----------|-----------|-------|
| `Lift` | `Lift[I,O](fn func(I)(O,error))` | Adapt error-returning fn to `func(ctx,I)(O,error)` |
| `LiftFallible` | `LiftFallible[I,O](fn func(I)(O,error))` | Alias for `Lift` |
| `LiftPure` | `LiftPure[I,O](fn func(I)O)` | Wrap infallible fn |
| `FilterFunc` | `FilterFunc[T](fn func(T)bool)` | Lift plain pred for use with free-fn `Filter` |
| `RejectFunc` | `RejectFunc[T](fn func(T)bool)` | Lift plain pred for use with free-fn `Reject` |
| `TapFunc` | `TapFunc[T](fn func(T))` | Lift void fn for use with free-fn `Tap` |
| `TapErrorFunc` | `TapErrorFunc(fn func(error))` | Lift void error observer for use with free-fn `TapError` |
| `FinallyFunc` | `FinallyFunc(fn func(error))` | Lift void cleanup function for use with free-fn `Finally` |
| `ExpandMapFunc` | `ExpandMapFunc[T](fn func(T)*Pipeline[T])` | Lift context-free child factory for use with free-fn `ExpandMap` |

---

## 15 · Pool

| Symbol | Signature | Notes |
|--------|-----------|-------|
| `NewPool[T]` | `NewPool[T](newFn func() T) *Pool[T]` | Construct a typed pool |
| `(p).Warmup` | `(p *Pool[T]).Warmup(n int)` | Pre-populate pool; reduces first-request latency |
| `(p).Put` | `(p *Pool[T]).Put(item *Pooled[T])` | Return item without calling `Release` |
| `MapPooled` | `MapPooled[I,O](p, pool, fn func(ctx,I,*Pooled[O])error, opts...)` | Acquire slot → call fn → emit `*Pooled[O]` |
| `ReleaseAll` | `ReleaseAll[T](items []*Pooled[T])` | Bulk release |
| `(item).Release` | `(item *Pooled[T]).Release()` | Return single item to pool |

---

## 16 · Run Options

`RunOption` values are passed to `Runner.Run(ctx, opts...)`, `Runner.RunAsync(ctx, opts...)`, or any terminal function.

| Option | Signature | Description |
|--------|-----------|-------------|
| `WithErrorStrategy` | `WithErrorStrategy(h ErrorHandler)` | Default error handler for all stages that do not set their own `OnError`. Priority: stage `OnError` > `WithErrorStrategy` > `Halt`. Does not apply to `DeadLetter` or `MapResult`. |
| `WithStore` | `WithStore(s Store)` | State backend for `MapWith`, `FlatMapWith`, `MapWithKey`, `FlatMapWithKey`. |
| `WithHook` | `WithHook(h Hook)` | Observability hook for the run. |
| `WithDrain` | `WithDrain(timeout time.Duration)` | Graceful shutdown: let in-flight items drain before stopping. |
| `WithCache` | `WithCache(cache Cache, ttl time.Duration)` | Default cache backend and TTL for `Map` stages using `CacheBy`. |
| `WithSampleRate` | `WithSampleRate(n int)` | `SampleHook.OnItemSample` frequency (default 10). Negative disables. |
| `WithCodec` | `WithCodec(c Codec)` | Serialisation codec for store-backed state and cache. Default: JSON. |
| `WithPauseGate` | `WithPauseGate(g *Gate)` | Attach an external gate for pause/resume control. |

---

## 17 · Observability

All hooks are wired into every stage runner automatically when provided via `WithHook`.

| Interface | Package | Notes |
|-----------|---------|-------|
| `Hook` | `internal` (re-exported) | Base: `OnStageStart`, `OnItem`, `OnStageDone` |
| `OverflowHook` | `internal` | `OnOverflow` fired on `DropNewest` / `DropOldest` |
| `SupervisionHook` | `internal` | `OnRestart` fired on supervision restart |
| `SampleHook` | `internal` | `OnItemSample` fired every N items (configurable via `WithSampleRate`) |
| `GraphHook` | `internal` | `OnGraph` fired at run-time with full `[]GraphNode` |
| `BufferHook` | `internal` | `OnBufferChange` fired on channel depth changes |
| `LogHook` | root | Structured log output for every hook event |
| `MultiHook` | root | Fan-out to multiple hook implementations |
| `MetricsHook` | root | Lock-free atomic counters per stage; `Snapshot()` / `Reset()` / `Snapshot().JSON()` |
| `JSONCodec` | root | Default codec for cache and store-backed `Ref` serialization |

**`GraphNode`** exposes: `Kind`, `Name`, `Concurrency`, `Buffer`, `Overflow`, `BatchSize`, `Timeout`, `HasRetry`, `HasSupervision`.

**`Pipeline[T].Describe() []GraphNode`**: returns the same `[]GraphNode` snapshot synchronously, without executing the pipeline. Callable on any `*Pipeline[T]`, including intermediate (non-terminal) stages. Useful for static validation and unit-testing graph structure without a full `Run`.

**`ContextCarrier`**: interface implemented by item types that carry a `context.Context` with an attached trace span. When an item implements `ContextCarrier`, the engine merges its context into the stage function call: cancellation still comes from the pipeline stage context, but context values (e.g. the active trace span) come from the item. Stage functions can call `tracer.Start(ctx, "my-work")` to create per-item child spans with no signature changes. Zero cost for items that don't implement the interface. See `tails/kotel` for OTel integration.

---

## 18 · DedupSet Backends

`WithDedupSet(s)` accepts any value implementing `DedupSet`:

| Backend | Constructor | Bounded Memory | False Positives | Expiry |
|---------|-------------|----------------|-----------------|--------|
| `MemoryDedupSet` | `MemoryDedupSet()` | No | No | No |
| `BloomDedupSet` | `BloomDedupSet(expectedItems, fp)` | Yes | Yes (configurable rate) | No |
| `TTLDedupSet` | `TTLDedupSet(ttl)` | Yes (bounded by active window) | No | Yes (lazy, per-key TTL) |

**Notes**
- `MemoryDedupSet` is the default for `Distinct`, `DistinctBy`, `Dedupe`, `DedupeBy`, and `ExpandMap`.
- `BloomDedupSet` panics if `expectedItems <= 0` or `falsePositiveRate` is not in `(0, 1)`.
- `TTLDedupSet` panics if `ttl <= 0`. Re-adding an existing key refreshes its expiry. Eviction is lazy (on next `Contains` or `Add`); no background goroutine is started.
- External backends (Redis, etc.) are available in `tails/kredis` via `kredis.NewDedupSet`.

---

## 19 · Testing Infrastructure

| Component | Notes |
|-----------|-------|
| `testkit.MustCollect` | Run a pipeline and collect output; `t.Fatal` on error |
| `testkit.CollectAndExpect` | Collect and assert exact ordered equality |
| `testkit.CollectAndExpectUnordered` | Collect and assert same multiset, any order |
| `testkit.MustRun` / `MustRunWithHook` | Run a sink runner; optionally capture a `RecordingHook` |
| `testkit.RecordingHook` | Captures `Items`, `Errors`, `Drops`, `Restarts`, `Graph`, `Dones` events |
| `testkit.NewTestClock` | Virtual clock for deterministic tests of time-sensitive operators |
| `testkit.FailAt` / `FailEvery` | Inject errors at specific item positions or intervals |
| `testkit.SlowMap` / `SlowSink` | Simulate slow I/O in map/sink functions |
| Property tests (`properties_test.go`) | Algebra invariants verified with `pgregory.net/rapid` (build tag: `property`); run via `task test:property`. Covers: `Merge` multiset/commutativity, `Sort` ordering/idempotence, `Take∘Sort` prefix semantics, `Broadcast` completeness, `Balance` item-count and round-robin fairness. |

---

## 20 · Tails (External Adapters)

Tails are separate Go modules under `tails/` that adapt external systems to kitsune pipelines. Each follows the "user owns the client" principle: the caller creates, configures, and closes connections; kitsune never opens or closes them. See `doc/tails.md` for detailed usage examples.

| Module | Package | Source | Sink | Notes |
|--------|---------|--------|------|-------|
| Apache Kafka | `tails/kkafka` | `Consume` | `Produce` | segmentio/kafka-go |
| NATS / JetStream | `tails/knats` | `Subscribe`, `Consume` | `Publish`, `JetStreamPublish` | nats.go; see also `tails/kjetstream` for pull, ordered, KV, and async publish |
| NATS JetStream (advanced) | `tails/kjetstream` | `Fetch`, `FetchBytes`, `OrderedConsume`, `WatchKV` | `PublishAsync` (returns sink + flush), `PutKV` | nats.go/jetstream; pull-batch, ordered consumers, KV watch, async batched publish |
| RabbitMQ / AMQP 0-9-1 | `tails/kamqp` | `Consume` | `Publish` | rabbitmq/amqp091-go; manual ack by default, configurable via `WithAutoAck`, `WithRequeueOnNack` |
| MQTT | `tails/kmqtt` | `Subscribe` | `Publish` | paho.mqtt.golang |
| Azure Service Bus | `tails/kazsb` | `Receive` | `Send` | azservicebus |
| Azure Event Hubs | `tails/kazeh` | `Receive` | `ProduceBatch` | azeventhubs |
| AWS SQS | `tails/ksqs` | `Receive` | `Send` | aws-sdk-go-v2 |
| AWS Kinesis | `tails/kkinesis` | `Consume` | `Put` | aws-sdk-go-v2 |
| Google Cloud Pub/Sub | `tails/kpubsub` | `Receive` | `Publish` | cloud.google.com/go/pubsub |
| Google Cloud Storage | `tails/kgcs` | `ListObjects` | `Upload` | cloud.google.com/go/storage |
| Apache Pulsar | `tails/kpulsar` | `Consume` | `Send` | apache/pulsar-client-go |
| Elasticsearch / OpenSearch | `tails/kes` | `Scroll` | `BulkIndex` | elastic/go-elasticsearch |
| MongoDB | `tails/kmongo` | `Watch`, `Find` | `Insert` | mongodb/mongo-go-driver |
| PostgreSQL | `tails/kpostgres` | `Listen`, `Query` | `Insert` | jackc/pgx |
| Redis | `tails/kredis` | `Subscribe` | `Publish` | redis/go-redis; also `Store` and `Cache` backends |
| ClickHouse | `tails/kclickhouse` | `Query` | `Insert` | ClickHouse/clickhouse-go |
| SQLite | `tails/ksqlite` | `Query` | `Insert` | mattn/go-sqlite3 |
| AWS S3 | `tails/ks3` | `ListObjects` | `Upload` | aws-sdk-go-v2 |
| gRPC | `tails/kgrpc` | `ServerStream` | `ClientStream` | google.golang.org/grpc |
| HTTP | `tails/khttp` | `Poll` | `Post` | net/http |
| WebSocket | `tails/kwebsocket` | `Receive` | `Send` | nhooyr.io/websocket |
| File | `tails/kfile` | `Lines`, `Watch` | `Write` | os/bufio |
| OpenTelemetry | `tails/kotel` | – | – | Hook only; traces + metrics |
| Prometheus | `tails/kprometheus` | – | – | Hook only; Prometheus metrics |
| Datadog | `tails/kdatadog` | – | – | Hook only; DogStatsD metrics |
