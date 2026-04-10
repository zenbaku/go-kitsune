# Operator Catalog

**Free functions** (`Map`, `FlatMap`, `Batch`, …) change the element type as items flow through. **Methods** (`.Filter`, `.Take`, `.Skip`, …) preserve it. This split is a Go constraint: methods can't introduce new type parameters, so anything that changes `Pipeline[A]` to `Pipeline[B]` must be a free function. Each intermediate variable documents what is flowing, and the compiler checks every transition.

---

## Sources

| Function | Description |
|---|---|
| `FromSlice[T](items []T)` | Emit each element of a slice |
| `From[T](ch <-chan T)` | Wrap an existing channel (e.g., Kafka consumer) |
| `Generate[T](fn)` | Push-based custom source; call `yield` per item, return when done |
| `FromIter[T](seq iter.Seq[T])` | Wrap a Go 1.23+ iterator |
| `NewChannel[T](buffer int)` | Push-based source for external producers (HTTP handlers, event loops); see [Channel[T]](#channelt--runasync) |
| `Ticker(d, opts…)` | Emit `time.Time` on every tick of `d`; stops when context is cancelled |
| `Interval(d, opts…)` | Emit a monotonically increasing `int64` (0, 1, 2, …) on every tick of `d` |
| `Unfold[S,T](seed S, fn)` | Generate from a seed: `fn(acc)` → `(value, nextAcc, stop)`; halt when `stop` is true |
| `Iterate[T](seed T, fn func(T) T)` | Infinite stream: emit `seed`, then `fn(seed)`, then `fn(fn(seed))`, … |
| `Repeatedly[T](fn func() T)` | Infinite stream: call `fn` on each iteration; use `Take` to bound |
| `Cycle[T](items []T)` | Infinite stream: loop over `items` forever; use `Take` to bound |
| `Timer[T](delay, fn, opts…)` | Emit exactly one value after `delay` by calling `fn`; stops if context is cancelled |
| `Concat[T](factories …func() *Pipeline[T])` | Run each factory sequentially, forwarding all items from each before starting the next |
| `Amb[T](factories…)` | Race multiple pipeline factories; forward items exclusively from whichever factory emits first, cancelling all others |

## 1:1 Transforms

| Function | Description |
|---|---|
| `Map[I,O](p, fn, opts…)` | Apply a function to each item, potentially changing the type |
| `MapWith[I,O,S](p, key, fn, opts…)` | `Map` with injected concurrent-safe state `*Ref[S]` |
| `Map` + `CacheBy(keyFn, opts…)` | `Map` with TTL-based cache; skips `fn` on cache hit |
| `MapRecover[I,O](p, fn, recover, opts…)` | `Map` that calls `recover(ctx, item, err)` instead of failing on error |
| `MapResult[I,O](p, fn, opts…)` | Map that routes success to `ok *Pipeline[O]` and errors to `failed *Pipeline[ErrItem[I]]`; no halt or retry |
| `DeadLetter[I,O](p, fn, opts…)` | Like `MapResult` but with retry embedded; exhausted failures route to `*Pipeline[ErrItem[I]]` |
| `DeadLetterSink[I](p, fn, opts…)` | Like `DeadLetter` for terminal sinks; returns `(*Pipeline[ErrItem[I]], *Runner)` |
| `MapEvery[T](p, nth, fn)` | Apply `fn` to every nth item (0-indexed); all other items pass through unchanged |
| `WithIndex[T](p)` | Pair each item with its 0-based stream position; emits `Pair[int, T]` |
| `Timestamp[T](p, opts…)` | Pair each item with the time it was observed; emits `Timestamped[T]{Value, Time}`; use `WithClock` for deterministic tests |
| `TimeInterval[T](p, opts…)` | Pair each item with the duration since the previous item; first item has `Elapsed == 0`; emits `TimedInterval[T]{Value, Elapsed}`; always `Concurrency(1)` |
| `Intersperse[T](p, sep T)` | Insert `sep` between consecutive items |
| `StartWith[T](p, items…)` | Prepend one or more items before the pipeline; emits the prefix then all items from `p` |
| `DefaultIfEmpty[T](p, defaultVal)` | Pass items through unchanged; if `p` produces no items, emit `defaultVal` instead |
| `MapIntersperse[T,O](p, sep O, fn)` | Apply `fn` to each item and insert `sep` between the results |

## 1:N Expansion

| Function | Description |
|---|---|
| `FlatMap[I,O](p, fn, opts…)` | Each input produces zero or more outputs |
| `ConcatMap[I,O](p, fn, opts…)` | Like `FlatMap` but always sequential (forces `Concurrency(1)`); guarantees emission order |
| `SwitchMap[I,O](p, fn, opts…)` | Like `FlatMap` but cancels the active inner pipeline when a new item arrives; only the latest item's output reaches downstream ("latest wins") |
| `ExhaustMap[I,O](p, fn, opts…)` | Like `FlatMap` but ignores new upstream items while an inner pipeline is active ("first wins") |
| `FlatMapWith[I,O,S](p, key, fn, opts…)` | `FlatMap` with injected concurrent-safe state `*Ref[S]` |
| `Pairwise[T](p)` | Emit consecutive overlapping pairs `Pair[T,T]`; first item buffers, then `{0,1},{1,2},{2,3},…` |
| `Unbatch[T](p)` | Flatten a `Pipeline[[]T]` back to individual items (inverse of `Batch`) |

## Batching & Windowing

| Function | Description |
|---|---|
| `Batch[T](p, size, opts…)` | Collect up to `size` items into a `[]T` slice; use `BatchTimeout` to flush partials |
| `Window[T](p, duration, opts…)` | Time-based batching: flush accumulated items every `duration` |
| `WindowByTime[T](p, duration)` | Fixed-duration tumbling windows: a new window starts every `duration` regardless of item arrival; partial window emitted when source completes |
| `SlidingWindow[T](p, size, step)` | Overlapping (size > step) or tumbling (size == step) count-based windows emitted as `[]T` |

## Enrichment

Enrichment operators bulk-fetch external data for a batch of items and attach it. Keys are deduplicated before each fetch call, so if multiple items share the same key, only one lookup is made.

| Function | Description |
|---|---|
| `MapBatch[I,O](p, size, fn, opts…)` | Collect up to `size` items, pass the slice to `fn`, flatten results back to individual items; sugar for `Batch`+`FlatMap` |
| `LookupBy[T,K,V](p, cfg)` | Bulk-fetch a value per item using `LookupConfig.Key`/`Fetch`; emits `Pair[T,V]`; use `ZipWith` for parallel lookups |
| `Enrich[T,K,V,O](p, cfg)` | Like `LookupBy` but calls `EnrichConfig.Join` to produce `O` directly, no intermediate `Pair` |

Config types:

```go
kitsune.LookupConfig[T, K, V]{Key, Fetch, BatchSize}
kitsune.EnrichConfig[T, K, V, O]{Key, Fetch, Join, BatchSize}
```

**Parallel lookups**: use `Broadcast` + two `LookupBy` calls + `ZipWith` to run independent fetches concurrently:

```go
branches   := kitsune.Broadcast(terms, 2)
withEntity := kitsune.LookupBy(branches[0], kitsune.LookupConfig[Term, int, Entity]{...})
withNames  := kitsune.LookupBy(branches[1], kitsune.LookupConfig[Term, int, []Name]{...})
enriched   := kitsune.ZipWith(withEntity, withNames,
    func(_ context.Context, e kitsune.Pair[Term, Entity], n kitsune.Pair[Term, []Name]) (EnrichedTerm, error) {
        return EnrichedTerm{Entity: e.Second, Names: n.Second}, nil
    },
)
```

## Filtering & Gating

| Method / Function | Description |
|---|---|
| `.Filter(fn func(T) bool)` | Keep only items where `fn` returns true |
| `Reject[T](p, fn)` | Keep only items where `fn` returns **false** (inverse of `Filter`) |
| `.Take(n int)` | Emit the first `n` items, then stop the pipeline |
| `TakeEvery[T](p, nth)` | Emit every nth item (0-indexed); `TakeEvery(p,2)` emits indices 0, 2, 4, … |
| `.Skip(n int)` | Drop the first `n` items, emit the rest |
| `DropEvery[T](p, nth)` | Drop every nth item (0-indexed); `DropEvery(p,2)` drops indices 0, 2, 4, … |
| `TakeWhile[T](p, fn)` | Emit while `fn` returns true; stop (and signal sources) on first false |
| `DropWhile[T](p, fn)` | Suppress while `fn` returns true; pass all items once `fn` returns false |

## Time-Based

| Function | Description |
|---|---|
| `Throttle[T](p, d, opts…)` | Emit the first item per window of `d`; drop items arriving within the cooldown |
| `Debounce[T](p, d, opts…)` | Emit only the last item after `d` of silence; each new arrival resets the timer |
| `RateLimit[T](p, rps, opts…)` | Token-bucket rate limiter; `Burst(n)` controls burst capacity; `RateMode(RateLimitWait)` (default) applies backpressure, `RateMode(RateLimitDrop)` silently discards excess items |

## Deduplication

| Function | Description |
|---|---|
| `Distinct[T comparable](p)` | Drop duplicate items; keeps first occurrence of each value |
| `DistinctBy[T](p, key func(T) string)` | Drop duplicates by derived key; in-memory, unbounded |
| `p.Dedupe(key, opts…)` | Drop items whose key is already seen; defaults to in-process `MemoryDedupSet`; pass `WithDedupSet(s)` for a custom backend |
| `ConsecutiveDedup[T comparable](p)` | Drop consecutive duplicate values; non-adjacent duplicates are kept |
| `ConsecutiveDedupBy[T,K](p, fn)` | Drop consecutive items with the same derived key |

## Aggregation & Reduction

| Function | Description |
|---|---|
| `Scan[T,S](p, initial S, fn func(S,T) S, opts…)` | Running accumulator; emits updated state after every item; always sequential |
| `Reduce[T,S](p, seed S, fn func(S,T) S)` | Fold entire stream into one value; emits once when input closes (emits `seed` on empty stream) |
| `ReduceWhile[T,S](ctx, p, seed S, fn)` | Like `Reduce` but `fn` returns `(newAcc, continue)`; pipeline stops (and `newAcc` is returned) when `continue` is false |
| `GroupBy[T,K](p, key func(T) K)` | Collect all items into a `map[K][]T` and emit once (bounded streams only) |
| `ChunkBy[T,K](p, fn)` | Group consecutive items with the same key into `[]T` slices (bounded streams only) |
| `ChunkWhile[T](p, fn func(prev,next T) bool)` | Group consecutive items while `fn(prev,next)` returns true (bounded streams only) |
| `Sort[T](p, less func(a,b T) bool)` | Buffer entire stream and emit in sorted order (bounded streams only) |
| `SortBy[T,K](p, key, less)` | Buffer entire stream and sort by derived key (bounded streams only) |

## Side Effects

| Method | Description |
|---|---|
| `.Tap(fn func(T))` | Call `fn` for each item as a side effect; passes items through unchanged |
| `.Through(fn func(*Pipeline[T]) *Pipeline[T])` | Apply a reusable, type-preserving pipeline fragment; accepts `Stage[T,T]` directly |

## Fan-out / Fan-in

| Function | Description |
|---|---|
| `Partition[T](p, fn)` | Route each item to `match` or `rest` based on predicate; exactly one output per item |
| `Broadcast[T](p, n)` | Copy every item to all `n` output pipelines |
| `Merge[T](ps…)` | Fan-in: combine multiple pipelines into one; works across independent graphs |
| `Zip[A,B](a, b)` | Pair items by position into `Pair[A,B]`; stops when the shorter input closes |
| `ZipWith[A,B,O](a, b, fn, opts…)` | Like `Zip` but applies `fn(a, b)` immediately, producing `O` directly without an intermediate `Pair` |
| `Unzip[A,B](p)` | Split a `Pipeline[Pair[A,B]]` into two pipelines `(*Pipeline[A], *Pipeline[B])`; inverse of `Zip` |
| `WithLatestFrom[A,B](primary, secondary)` | Combine each primary item with the most recent secondary value; drops primary items until secondary emits |
| `CombineLatest[A,B](a, b)` | Like `WithLatestFrom` but symmetric: either side emitting triggers output, paired with the latest value from the other side; no output until both sides have emitted |
| `CombineLatestWith[A,B,O](a, b, fn, opts…)` | Like `CombineLatest` but applies `fn(a, b)` immediately, producing `O` directly |
| `Balance[T](p, n)` | Round-robin fan-out: each item goes to exactly one of `n` output pipelines; complements `Broadcast` (copy to all) and `Partition` (split by predicate) |

## Terminals

| Method / Function | Returns | Description |
|---|---|---|
| `.ForEach(fn, opts…)` | `*Runner` | Process each item; call `.Run(ctx)` to execute |
| `.Drain()` | `*Runner` | Consume and discard all items |
| `runner.RunAsync(ctx, opts…)` | `*RunHandle` | Start pipeline in background; call `.Wait()`, select on `.Done()`, or read `.Err()` |
| `.Iter(ctx, opts…)` | `(iter.Seq[T], func() error)` | Return a pull-based iterator (range-over-func); call the error function after the loop |
| `.Collect(ctx, opts…)` | `([]T, error)` | Run and materialize all items into a slice |
| `.First(ctx, opts…)` | `(T, bool, error)` | Run and return the first item; `false` if stream is empty |
| `.Last(ctx, opts…)` | `(T, bool, error)` | Run and return the final item; `false` if stream is empty |
| `.Count(ctx, opts…)` | `(int64, error)` | Run and return the total number of items emitted |
| `.Any(ctx, fn, opts…)` | `(bool, error)` | Run and return `true` if any item satisfies `fn`; stops early on first match |
| `.All(ctx, fn, opts…)` | `(bool, error)` | Run and return `true` if every item satisfies `fn`; stops early on first mismatch |
| `Find[T](ctx, p, pred)` | `(T, bool, error)` | Return the first item satisfying `pred`; stops the pipeline early; `false` if no match |
| `Sum[T Numeric](ctx, p)` | `(T, error)` | Sum all items; returns zero on an empty stream |
| `Min[T](ctx, p, less)` | `(T, bool, error)` | Smallest item; `false` if stream is empty |
| `Max[T](ctx, p, less)` | `(T, bool, error)` | Largest item; `false` if stream is empty |
| `MinMax[T](ctx, p, less)` | `(Pair[T,T], bool, error)` | Both min and max in one pass; `false` if empty |
| `MinBy[T,K](ctx, p, key, less)` | `(T, bool, error)` | Item with the smallest derived key |
| `MaxBy[T,K](ctx, p, key, less)` | `(T, bool, error)` | Item with the largest derived key |
| `Frequencies[T comparable](ctx, p)` | `(map[T]int, error)` | Count occurrences of each distinct item |
| `FrequenciesBy[T,K](ctx, p, key)` | `(map[K]int, error)` | Count occurrences of each distinct key |
| `Contains[T comparable](ctx, p, value)` | `(bool, error)` | Returns `true` if any item equals `value`; stops early on first match |
| `(p).ElementAt(ctx, index)` | `(T, bool, error)` | Returns the item at 0-based `index`; `(zero, false, nil)` if the stream is shorter |
| `ToMap[T,K,V](ctx, p, key, value)` | `(map[K]V, error)` | Collect stream into `map[K]V`; duplicate keys: last value wins |
| `SequenceEqual[T comparable](ctx, a, b)` | `(bool, error)` | Returns `true` if both pipelines emit the same items in the same order (finite streams) |
| `CountBy[T](p, keyFn, opts…)` | `*Pipeline[map[string]int64]` | Count occurrences by key; emits a `map[string]int64` snapshot after each item; compose with `Throttle` for periodic output |
| `SumBy[T,V](p, keyFn, valueFn, opts…)` | `*Pipeline[map[string]V]` | Accumulate a numeric value by key; emits a `map[string]V` snapshot after each item |
| `ReduceWhile[T,S](ctx, p, seed, fn)` | `(S, error)` | Fold with early termination; `fn` returns `(newAcc, continue)` |
| `TakeRandom[T](ctx, p, n)` | `([]T, error)` | Uniform random sample of `n` items (reservoir sampling); all items if n ≥ stream length |

---

## Channel[T] + RunAsync

`Channel[T]` is a push-based source for when external code drives item arrival: HTTP handlers, CLI loops, event bridges.

```go
src := kitsune.NewChannel[string](256)
h   := kitsune.Map(src.Source(), parse).ForEach(store).RunAsync(ctx)

http.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
    if err := src.Send(r.Context(), readBody(r)); err != nil {
        http.Error(w, err.Error(), 500)
    }
})
src.Close()    // signal no more items (e.g. on server shutdown)
h.Wait()       // block until pipeline drains; returns error if one occurred
```

| Symbol | Description |
|---|---|
| `NewChannel[T](buffer int)` | Create a push source with the given buffer size |
| `(c) Source() *Pipeline[T]` | Wire into a pipeline; panics if called more than once |
| `(c) Send(ctx, item) error` | Block until buffer has space; returns `ErrChannelClosed` or `ctx.Err()` |
| `(c) TrySend(item) bool` | Non-blocking send; returns `false` if buffer is full or channel is closed |
| `(c) Close()` | Idempotent close; pipeline drains remaining items then exits |
| `ErrChannelClosed` | Returned by `Send` when the channel has been closed |
| `runner.RunAsync(ctx)` | Start pipeline in background goroutine; returns `*RunHandle` |
| `RunHandle.Wait()` | Block until the pipeline finishes; returns nil or an error |
| `RunHandle.Done()` | `<-chan struct{}` closed when the pipeline finishes |
| `RunHandle.Err()` | `<-chan error` that receives exactly one value (nil or error) |
| `RunHandle.Pause()` | Stop sources from emitting; in-flight items continue draining. Idempotent |
| `RunHandle.Resume()` | Allow sources to emit again after a pause. Idempotent |
| `RunHandle.Paused()` | Report whether the pipeline is currently paused |
| `NewGate()` | Create a standalone `Gate` in the open (unpaused) state |
| `WithPauseGate(g)` | Run option: attach an external `Gate` for pause/resume via `Runner.Run` |

---

## Stage[I, O] + Then

`Stage[I,O]` is a named function type for reusable pipeline fragments. Define once, compose with `Then`, test with `FromSlice`, run in production with a `Channel[T]` source.

```go
var ParseStage  kitsune.Stage[string, Event]        = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[Event]        { ... }
var EnrichStage kitsune.Stage[Event, EnrichedEvent] = func(p *kitsune.Pipeline[Event]) *kitsune.Pipeline[EnrichedEvent] { ... }

var FullPipeline = kitsune.Then(ParseStage, EnrichStage)  // Stage[string, EnrichedEvent]

// Test any stage independently — no goroutines, no Channel needed:
events, _ := ParseStage.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
```

| Symbol | Description |
|---|---|
| `Stage[I, O any]` | Named function type `func(*Pipeline[I]) *Pipeline[O]`; zero runtime cost |
| `(s) Apply(p *Pipeline[I]) *Pipeline[O]` | Run this stage against an input pipeline |
| `(s) Or(fallback Stage[I,O]) Stage[I,O]` | Return a stage that tries `s` first; if the primary produces no output (error or empty), `fallback` is called with the same input |
| `Then[A,B,C](first, second)` | Compose two stages into one; free function required (Go methods cannot introduce new type parameters) |

`Stage[T, T]` is directly compatible with `.Through()`, no adapter needed:

```go
var Validate kitsune.Stage[Order, Order] = func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
    return kitsune.Filter(p, kitsune.FilterFunc(isValid))
}

orders.Through(Validate)        // existing Through API
Validate.Apply(orders)          // Stage.Apply — identical result
kitsune.Then(Validate, enrich)  // compose with a downstream stage
```

### Generic middleware

A Stage factory function parameterised over `T` acts as generic middleware, one definition for any item type:

```go
// WithLogging works for Pipeline[string], Pipeline[Event], Pipeline[Result], etc.
func WithLogging[T any](label string) kitsune.Stage[T, T] {
    return func(p *kitsune.Pipeline[T]) *kitsune.Pipeline[T] {
        return kitsune.Tap(p, kitsune.TapFunc(func(item T) { slog.Info(label, "item", item) }))
    }
}

// Apply to stages of different types without duplication:
var Parse  = kitsune.Then(WithLogging[string]("raw"),   parseStage)
var Enrich = kitsune.Then(WithLogging[Event]("parsed"), enrichStage)
```

### Parameterised (struct-based) stages

For stages that need configuration, use a struct with an `AsStage()` method:

```go
type EnrichStage struct {
    Concurrency int
    Cache       kitsune.Cache
    TTL         time.Duration
}

func (s *EnrichStage) AsStage() kitsune.Stage[Event, EnrichedEvent] {
    return func(p *kitsune.Pipeline[Event]) *kitsune.Pipeline[EnrichedEvent] {
        return kitsune.Map(p, enrich,
            kitsune.CacheBy(eventKey, kitsune.CacheBackend(s.Cache), kitsune.CacheTTL(s.TTL)),
            kitsune.Concurrency(s.Concurrency))
    }
}

enrich := (&EnrichStage{Concurrency: 20, Cache: cache, TTL: 5 * time.Minute}).AsStage()
full   := kitsune.Then(ParseStage, enrich)
```

### Swappable sources

The same `Stage` runs against different sources — `FromSlice` for tests, `Channel[T]` for production:

```go
var FullPipeline = kitsune.Then(ParseStage, EnrichStage) // Stage[string, Result]

// Test — no goroutines, no timing, fully deterministic:
results, _ := FullPipeline.Apply(kitsune.FromSlice(testFixtures)).Collect(ctx)

// Production — accept pushes from HTTP handlers or event streams:
src := kitsune.NewChannel[string](256)
h   := FullPipeline.Apply(src.Source()).ForEach(store).Build().RunAsync(ctx)
// ... send items ...
h.Wait()
```

See [`examples/stages/`](../examples/stages/) for a runnable version covering all four patterns.

---

## State

| Symbol | Description |
|---|---|
| `NewKey[T](name, initial T, opts…)` | Declare a typed, named state key with an initial value |
| `MapWith`, `FlatMapWith` | Transforms that inject a concurrent-safe `*Ref[S]` for the key |
| `MapWithKey[I,O,S](p, keyFn, key, fn, opts…)` | Like `MapWith` but partitions state by a key extracted from each item; each distinct key gets its own independent `Ref` |
| `FlatMapWithKey[I,O,S](p, keyFn, key, fn, opts…)` | Like `FlatMapWith` but with per-key state partitioning |
| `StateTTL(d)` | `NewKey` option: expire state after `d` of inactivity; lazy expiry on read; reset to initial value on expiry |
| `Ref[T].Get(ctx)` | Read current state value |
| `Ref[T].Set(ctx, v)` | Overwrite state value |
| `Ref[T].Update(ctx, fn)` | Atomic read-modify-write |
| `Ref[T].GetOrSet(ctx, fn)` | Return current value; for store-backed Refs calls fn if key is absent |
| `Ref[T].UpdateAndGet(ctx, fn)` | Atomic read-modify-write; returns the new value |
| `MemoryStore()` | In-process state backend (default) |

---

## Stage Options

| Option | Description |
|---|---|
| `Concurrency(n)` | Run `n` parallel workers for this stage (default: 1) |
| `Ordered()` | Preserve input order when `Concurrency > 1` |
| `Buffer(n)` | Output channel buffer size (default: 16) |
| `Overflow(strategy)` | `Block` (default), `DropNewest`, or `DropOldest` when buffer is full |
| `WithName(name)` | Label for metrics, traces, and debugging |
| `OnError(handler)` | Per-stage error policy (default: `Halt`) |
| `BatchTimeout(d)` | Flush a partial batch after `d` (use with `Batch`) |
| `Timeout(d)` | Per-item deadline for `Map` and `FlatMap`; each attempt gets a fresh timeout |
| `WithClock(c)` | Override the time source for time-sensitive stages (`Window`, `Batch`, `Throttle`, `Debounce`) and sources (`Ticker`, `Interval`, `Timer`); pass `testkit.NewTestClock()` for deterministic, sleep-free tests |
| `Supervise(policy)` | Per-stage restart and panic-recovery policy |

---

## Fault Tolerance

| Symbol | Description |
|---|---|
| `CircuitBreaker[I,O](p, fn, opts…)` | Closed → Open → Half-Open state machine; `FailureThreshold(n)` sets consecutive-failure limit; `CooldownDuration(d)` controls recovery wait; `HalfOpenProbes(n)` sets probe count; `HalfOpenTimeout(d)` limits probe duration; rejected items return `ErrCircuitOpen` (compose with `OnError(Skip())` to drop) |
| `ErrCircuitOpen` | Sentinel error returned when the circuit is open |

## Error Handling

| Symbol | Description |
|---|---|
| `Halt()` | Stop pipeline on first error (default) |
| `Skip()` | Drop failing item and continue |
| `Retry(n, backoff)` | Retry up to `n` times, then halt |
| `RetryThen(n, backoff, fallback)` | Retry up to `n` times, then delegate to `fallback` |
| `Return(val)` | Replace the failing item with `val` and continue; compose with `RetryThen` for retry-then-default patterns |
| `FixedBackoff(d)` | Constant wait between retries |
| `ExponentialBackoff(initial, max)` | Doubling backoff capped at `max` |

## Supervision

| Symbol | Description |
|---|---|
| `RestartOnError(n, backoff)` | Restart stage on errors up to `n` times; panics propagate |
| `RestartOnPanic(n, backoff)` | Restart stage on panics up to `n` times; errors halt |
| `RestartAlways(n, backoff)` | Restart on both errors and panics up to `n` times |
| `PanicPropagate` / `PanicRestart` / `PanicSkip` | Panic disposition constants |

---

## Observability

| Symbol | Description |
|---|---|
| `Hook` interface | `OnStageStart`, `OnItem`, `OnStageDone`; base lifecycle events |
| `OverflowHook` | Optional extension: `OnDrop` called when items are dropped |
| `SupervisionHook` | Optional extension: `OnStageRestart` called on stage restart |
| `SampleHook` | Optional extension: `OnItemSample` called for ~every 10th item |
| `GraphHook` | Optional extension: `OnGraph` called once with the full DAG topology |
| `BufferHook` | Optional extension: `OnBuffers` called with a channel fill-level query fn |
| `NewMetricsHook()` | Built-in zero-config hook; implements all five hook interfaces; lock-free atomic counters per stage; call `Stage(name)` for counts/latency or `Snapshot()` for a JSON-serializable point-in-time capture |
| `StageMetrics` | `Processed`, `Errors`, `Skipped`, `Dropped`, `Restarts`, `TotalNs`, `MinNs`, `MaxNs`; helpers `Throughput(elapsed)`, `MeanLatency()`, `ErrorRate()` |
| `LogHook(logger)` | Structured logging via `slog` |
| `MultiHook(hooks…)` | Compose multiple hooks; each sub-hook receives all events it implements |
| `WithHook(h)` | Run option: attach a hook to a pipeline run |

---

## Memory Pooling

Plain `Map` allocates a new output object on the heap for every item. At high throughput (millions of items/sec) this creates GC pressure that shows up as latency spikes. `MapPooled` eliminates the per-item allocation by drawing pre-allocated objects from a `sync.Pool` and handing them directly to your transform function.

Use pooling when profiling shows allocation as a bottleneck, or when the output type is large (e.g. byte buffers, structs with pre-allocated slices). For small output types the overhead of pool management usually outweighs the saving.

```go
pool := kitsune.NewPool(func() *MyStruct { return &MyStruct{Buf: make([]byte, 0, 4096)} })

results, _ := kitsune.MapPooled(src, pool,
    func(ctx context.Context, item Input, obj *MyStruct) (*MyStruct, error) {
        obj.Buf = obj.Buf[:0] // reset before reuse
        obj.Buf = append(obj.Buf, process(item)...)
        return obj, nil
    },
).Collect(ctx)

kitsune.ReleaseAll(results) // return every object to the pool when done
```

| Symbol | Description |
|---|---|
| `Pool[T]` / `NewPool(factory)` | Generic `sync.Pool` wrapper; `Get()` and `Put(v)` are safe for concurrent use |
| `MapPooled[I,O](p, pool, fn, opts…)` | Acquire a pre-allocated `O` from `pool`, call `fn(ctx, item, obj)`, emit `Pooled[O]`; call `Pooled[O].Release()` or `ReleaseAll(items)` after consumption |
| `Pooled[T]` | Wraps a pool-owned value; `Value T` is the payload; `Release()` returns it to the pool (idempotent) |
| `ReleaseAll[T](items)` | Bulk-release a `[]Pooled[T]` slice back to its pool after `Collect` |

---

## Helpers & Run Options

| Symbol | Description |
|---|---|
| `Lift[I,O](fn)` | Wrap a context-free `func(I)(O,error)` for use with `Map`/`FlatMap` |
| `LiftPure[I,O](fn)` | Wrap a context-free, error-free `func(I) O` for use with `Map`/`FlatMap`; unlike `Lift`, the wrapped function never fails |
| `Timestamped[T]` | Output type of `Timestamp`: `{Value T; Time time.Time}` |
| `TimedInterval[T]` | Output type of `TimeInterval`: `{Value T; Elapsed time.Duration}` |
| `Pair[A,B]` | Output type of `Zip` and `WithLatestFrom`: `{First A; Second B}` |
| `ErrItem[I]` | Output type of `MapResult`/`DeadLetter` failed branch: `{Item I; Err error}` |
| `StageError` | Error type returned by `Runner.Run`; carries `Stage`, `Attempt`, and `Cause` fields; unwrappable with `errors.As` |
| `WithDedupSet(s)` | Stage option: custom `DedupSet` backend for `Dedupe` (e.g. Redis-backed) |
| `MergeRunners(runners…)` | Combine forked terminal branches into a single `Runner`; returns `(*Runner, error)` |
| `WithStore(s)` | Run option: set the state backend (default: `MemoryStore`) |
| `WithDrain(timeout)` | Run option: graceful shutdown; drains in-flight items before exit |
| `WithCache(cache, ttl)` | Run option: default cache backend and TTL for all `Map`+`CacheBy` stages |
| `WithSampleRate(n)` | Run option: `OnItemSample` fires every nth item (default 10); pass ≤0 to disable |
| `WithCodec(c)` | Run option: custom serialisation codec for `Store`-backed state and `CacheBy` (default: JSON) |
