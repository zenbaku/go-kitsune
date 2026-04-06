# Kitsune

> **Note:** This project is an AI code exploration — the codebase is partially written using AI agents as an experiment in AI-assisted software development.

Type-safe, concurrent data pipelines for Go. Compose functions into stages; channels, goroutines, backpressure, and error routing are handled for you.

![Kitsune Inspector — live pipeline view](doc/screenshoot.png)

```go
lines  := kitsune.FromSlice(rawLines)
parsed := kitsune.Map(lines, parse)
err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
```

## Install

```
go get github.com/zenbaku/go-kitsune
```

## When to use Kitsune

Kitsune is a good fit for **in-process data pipelines** where you want typed composition, backpressure, and concurrency control without boilerplate:

- **ETL and data processing**: read from files, APIs, or databases; transform; write to sinks
- **Fan-out workflows**: partition, broadcast, or duplicate streams with compile-time type safety
- **Concurrent enrichment**: call external services with bounded parallelism and automatic retry
- **Streaming aggregation**: batch, window, deduplicate, or cache in-flight data

Kitsune is **not** a distributed stream processor. No Kafka consumer group management, no checkpointing, no cluster coordination. For distributed processing, use a dedicated framework. Kitsune complements them: handle the in-process pipeline logic here and connect to external systems via `tails/` or your own `Generate`/`ForEach` stages.

**New to Kitsune?** Start with the [Getting Started guide](doc/getting-started.md): first pipeline, concurrency, error handling, and testing in ~10 minutes.

For a comparison with other Go pipeline and streaming libraries (conc, go-streams, RxGo, Watermill, Benthos, Machinery), see the [Comparison Guide](doc/comparison.md).

## Live inspector dashboard

The `inspector` sub-package serves a real-time web dashboard. Add one line to any pipeline, no other changes needed.

```
go get github.com/zenbaku/go-kitsune/inspector
```

```go
insp := inspector.New()
defer insp.Close()
fmt.Println("Inspector:", insp.URL()) // open in browser

err := valid.ForEach(store, kitsune.WithName("store")).Run(ctx, kitsune.WithHook(insp))
```

```
+--------------------------------------------------------------+
|  Kitsune Inspector      [Stop] [Restart]  [●Light] [● SSE]  |
|--------------------------------------------------------------|
|  Uptime: 12s   Items: 24,601   Throughput: 3,241/s   4/4 ▶  |
|--------------------------------------------------------------|
|  [source] ──▶ [parse] ──▶ [validate] ──▶ [store]            |
|                  │                                           |
|                  └──▶ [deadletter]                           |
|--------------------------------------------------------------|
|  Stage     │ Items  │ Tput    │ Latency │ Errors │ Buffer   |
|  source    │ 24601  │ 3241/s  │ 0.2ms   │   0    │ [====  ] |
|  parse     │ 24558  │ 3200/s  │ 1.1ms   │   43   │ [=====  ]|
|  validate  │ 18321  │ 2280/s  │ 0.8ms   │    0   │ [===    ]|
|  store     │ 18321  │ 2100/s  │ 4.2ms   │    0   │ [=======]|
+--------------------------------------------------------------+
```

| Feature | Description |
|---|---|
| Pipeline DAG | Live SVG graph with BFS layout, animated edges, node status glow, zoom and pan |
| KPI strip | Uptime, total items, throughput, active stage count |
| Stage metrics table | Per-stage items, errors, drops, restarts, avg latency, sparkline, buffer fill bar |
| Detail sidebar | Click any stage node: 8-metric grid, config details, recent item samples |
| Event log | Color-coded lifecycle events (starts, completions, errors, restarts) |
| Stop / Restart | UI buttons wired to `CancelCh`/`RestartCh` for run-loop control |
| Dark / Light theme | Toggle via header button; preference saved in browser |

Try it: `task inspector` or `go run ./examples/inspector`

See [`doc/inspector.md`](doc/inspector.md) for the full dashboard reference including stop/restart wiring.

## Operator catalog

**Free functions** (`Map`, `FlatMap`, `Batch`, …) change the element type as items flow through. **Methods** (`.Filter`, `.Take`, `.Skip`, …) preserve it. This split is a Go constraint: methods can't introduce new type parameters, so anything that changes `Pipeline[A]` to `Pipeline[B]` must be a free function. Each intermediate variable documents what is flowing, and the compiler checks every transition.

### Sources

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

### 1:1 Transforms

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

### 1:N Expansion

| Function | Description |
|---|---|
| `FlatMap[I,O](p, fn, opts…)` | Each input produces zero or more outputs |
| `ConcatMap[I,O](p, fn, opts…)` | Like `FlatMap` but always sequential (forces `Concurrency(1)`); guarantees emission order |
| `SwitchMap[I,O](p, fn, opts…)` | Like `FlatMap` but cancels the active inner pipeline when a new item arrives; only the latest item's output reaches downstream ("latest wins") |
| `ExhaustMap[I,O](p, fn, opts…)` | Like `FlatMap` but ignores new upstream items while an inner pipeline is active ("first wins") |
| `FlatMapWith[I,O,S](p, key, fn, opts…)` | `FlatMap` with injected concurrent-safe state `*Ref[S]` |
| `Pairwise[T](p)` | Emit consecutive overlapping pairs `Pair[T,T]`; first item buffers, then `{0,1},{1,2},{2,3},…` |
| `Unbatch[T](p)` | Flatten a `Pipeline[[]T]` back to individual items (inverse of `Batch`) |

### Batching & Windowing

| Function | Description |
|---|---|
| `Batch[T](p, size, opts…)` | Collect up to `size` items into a `[]T` slice; use `BatchTimeout` to flush partials |
| `Window[T](p, duration, opts…)` | Time-based batching: flush accumulated items every `duration` |
| `WindowByTime[T](p, duration)` | Fixed-duration tumbling windows: a new window starts every `duration` regardless of item arrival; partial window emitted when source completes |
| `SlidingWindow[T](p, size, step)` | Overlapping (size > step) or tumbling (size == step) count-based windows emitted as `[]T` |

### Enrichment

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

### Filtering & Gating

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

### Time-Based

| Function | Description |
|---|---|
| `Throttle[T](p, d, opts…)` | Emit the first item per window of `d`; drop items arriving within the cooldown |
| `Debounce[T](p, d, opts…)` | Emit only the last item after `d` of silence; each new arrival resets the timer |
| `RateLimit[T](p, rps, opts…)` | Token-bucket rate limiter; `Burst(n)` controls burst capacity; `RateMode(RateLimitWait)` (default) applies backpressure, `RateMode(RateLimitDrop)` silently discards excess items |

### Deduplication

| Function | Description |
|---|---|
| `Distinct[T comparable](p)` | Drop duplicate items; keeps first occurrence of each value |
| `DistinctBy[T](p, key func(T) string)` | Drop duplicates by derived key; in-memory, unbounded |
| `p.Dedupe(key, opts…)` | Drop items whose key is already seen; defaults to in-process `MemoryDedupSet`; pass `WithDedupSet(s)` for a custom backend |
| `ConsecutiveDedup[T comparable](p)` | Drop consecutive duplicate values; non-adjacent duplicates are kept |
| `ConsecutiveDedupBy[T,K](p, fn)` | Drop consecutive items with the same derived key |

### Aggregation & Reduction

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

### Side Effects

| Method | Description |
|---|---|
| `.Tap(fn func(T))` | Call `fn` for each item as a side effect; passes items through unchanged |
| `.Through(fn func(*Pipeline[T]) *Pipeline[T])` | Apply a reusable, type-preserving pipeline fragment; accepts `Stage[T,T]` directly |

### Fan-out / Fan-in

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

### Terminals

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

### Channel[T] + RunAsync

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

### Stage[I, O] + Then

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

#### Generic middleware

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

#### Parameterised (struct-based) stages

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

#### Swappable sources

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

See [`examples/stages/`](examples/stages/) for a runnable version covering all four patterns.

### State

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

### Stage Options

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

### Fault Tolerance

| Symbol | Description |
|---|---|
| `CircuitBreaker[I,O](p, fn, opts…)` | Closed → Open → Half-Open state machine; `FailureThreshold(n)` sets consecutive-failure limit; `CooldownDuration(d)` controls recovery wait; `HalfOpenProbes(n)` sets probe count; `HalfOpenTimeout(d)` limits probe duration; rejected items return `ErrCircuitOpen` (compose with `OnError(Skip())` to drop) |
| `ErrCircuitOpen` | Sentinel error returned when the circuit is open |

### Error Handling

| Symbol | Description |
|---|---|
| `Halt()` | Stop pipeline on first error (default) |
| `Skip()` | Drop failing item and continue |
| `Retry(n, backoff)` | Retry up to `n` times, then halt |
| `RetryThen(n, backoff, fallback)` | Retry up to `n` times, then delegate to `fallback` |
| `Return(val)` | Replace the failing item with `val` and continue; compose with `RetryThen` for retry-then-default patterns |
| `FixedBackoff(d)` | Constant wait between retries |
| `ExponentialBackoff(initial, max)` | Doubling backoff capped at `max` |

### Supervision

| Symbol | Description |
|---|---|
| `RestartOnError(n, backoff)` | Restart stage on errors up to `n` times; panics propagate |
| `RestartOnPanic(n, backoff)` | Restart stage on panics up to `n` times; errors halt |
| `RestartAlways(n, backoff)` | Restart on both errors and panics up to `n` times |
| `PanicPropagate` / `PanicRestart` / `PanicSkip` | Panic disposition constants |

### Observability

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

### Memory Pooling

Plain `Map` allocates a new output object on the heap for every item. At high
throughput (millions of items/sec) this creates GC pressure that shows up as
latency spikes. `MapPooled` eliminates the per-item allocation by drawing
pre-allocated objects from a `sync.Pool` and handing them directly to your
transform function. The caller is responsible for returning objects to the pool
once done: either item-by-item with `Pooled[T].Release()` inside `ForEach`, or
in bulk with `ReleaseAll` after `Collect`.

Use pooling when profiling shows allocation as a bottleneck, or when the output
type is large (e.g. byte buffers, structs with pre-allocated slices). For small
output types the overhead of pool management usually outweighs the saving.

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

### Helpers & Run Options

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

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "strconv"

    kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
    input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})
    parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))
    doubled := kitsune.Map(parsed, func(_ context.Context, n int) (int, error) {
        return n * 2, nil
    })

    results, err := doubled.Filter(func(n int) bool { return n > 4 }).
        Collect(context.Background())
    if err != nil {
        panic(err)
    }
    fmt.Println(results) // [6 8 10]
}
```

## Examples

### Concurrent batched processing

```go
raw      := kitsune.From(kafkaStream)
orders   := kitsune.Map(raw, parseOrder)
enriched := kitsune.Map(orders, enrichWithCustomer, kitsune.Concurrency(20))
batched  := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
err      := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

### Fan-out with Partition

```go
orders := kitsune.FromSlice(allOrders)
high, regular := kitsune.Partition(orders, func(o Order) bool { return o.Amount >= 100 })

vip := kitsune.Map(high, priorityProcess).ForEach(notifyVIP)
std := kitsune.Map(regular, standardProcess).ForEach(store)

merged, err := kitsune.MergeRunners(vip, std)
if err != nil { /* handle */ }
err = merged.Run(ctx)
```

### Shared state across stages

```go
var queryOrigin = kitsune.NewKey[map[string]string]("origins", make(map[string]string))

items   := kitsune.FromSlice(records)
queries := kitsune.FlatMapWith(items, queryOrigin, buildQueries)   // writes to map
results := kitsune.FlatMap(queries, runSearch, kitsune.Concurrency(20))
final   := kitsune.MapWith(results, queryOrigin, correlate)        // reads from map

err := final.ForEach(sendToSQS).Run(ctx, kitsune.WithStore(kredis.NewStore(rdb, "app:")))
```

### Error handling with retry

```go
results := kitsune.Map(queries, callExternalAPI,
    kitsune.Concurrency(20),
    kitsune.OnError(kitsune.RetryThen(3,
        kitsune.ExponentialBackoff(time.Second, 30*time.Second),
        kitsune.Skip(),
    )),
    kitsune.WithName("external-api"),
)
```

### Mailbox overflow for real-time streams

```go
// Drop newest arrivals when the buffer is full — never block the producer.
kitsune.Map(sensorStream, process,
    kitsune.Buffer(64),
    kitsune.Overflow(kitsune.DropNewest),
    kitsune.WithName("sensor-stage"),
)

// Drop oldest buffered items — always keep the freshest data.
kitsune.Map(sensorStream, process,
    kitsune.Buffer(64),
    kitsune.Overflow(kitsune.DropOldest),
)
```

### Ordered concurrent output

```go
// Process in parallel but emit in input order.
results := kitsune.Map(records, enrichFromAPI,
    kitsune.Concurrency(20),
    kitsune.Ordered(),
)
```

### Supervision and panic recovery

```go
// Restart the stage up to 5 times on transient errors.
processed := kitsune.Map(records, callExternalAPI,
    kitsune.Supervise(kitsune.RestartOnError(5, kitsune.ExponentialBackoff(time.Second, 30*time.Second))),
)

// Recover from panics; treat them as restartable errors.
resilient := kitsune.Map(records, riskyTransform,
    kitsune.Supervise(kitsune.RestartOnPanic(3, kitsune.FixedBackoff(time.Second))),
)
```

### Deduplication and caching

```go
// Dedupe is a method — defaults to MemoryDedupSet; pass a Redis-backed set for distributed pipelines.
deduped := input.Dedupe(func(e Event) string { return e.ID })
deduped := input.Dedupe(func(e Event) string { return e.ID }, kitsune.WithDedupSet(redisDedupSet))

// CacheBy is a StageOption on Map — use runner-level defaults or override per-stage.
cached := kitsune.Map(items, expensiveLookup,
    kitsune.CacheBy(func(i Item) string { return i.Key },
        kitsune.CacheBackend(kitsune.MemoryCache(10000)),
        kitsune.CacheTTL(5*time.Minute),
    ),
)

// Set runner-level defaults so individual stages can just use CacheBy(keyFn).
runner.Run(ctx, kitsune.WithCache(myCache, 10*time.Minute))
```

### Generative and infinite sources

```go
// Unfold — produce values from a seed; halt when the function says so.
pages := kitsune.Unfold("", func(cursor string) (Page, string, bool) {
    page, next, err := api.Fetch(ctx, cursor)
    if err != nil || next == "" {
        return page, "", true // halt
    }
    return page, next, false
})

// Iterate — emit a seed, then apply fn repeatedly.
backoff := kitsune.Iterate(time.Second, func(d time.Duration) time.Duration {
    return min(d*2, 30*time.Second) // exponential backoff values
}).Take(8)

// Cycle — round-robin over a fixed set.
workers := kitsune.Cycle([]string{"worker-1", "worker-2", "worker-3"})

// Concat — sequential multi-source pipelines without cross-graph constraints.
all := kitsune.Concat(
    func() *kitsune.Pipeline[Record] { return fetchPage(ctx, 1) },
    func() *kitsune.Pipeline[Record] { return fetchPage(ctx, 2) },
    func() *kitsune.Pipeline[Record] { return fetchPage(ctx, 3) },
)
```

### Chunking and sorting

```go
// ChunkBy — group consecutive items that share the same key.
// Useful for processing run-length encoded data or pre-sorted streams.
chunks := kitsune.ChunkBy(events, func(e Event) string { return e.UserID })

// ChunkWhile — group consecutive items while a pairwise predicate holds.
// Use it to detect contiguous ranges or runs.
ranges := kitsune.ChunkWhile(sortedIDs, func(prev, next int) bool {
    return next-prev == 1 // contiguous integer range
})

// Sort / SortBy — buffer and re-emit in sorted order (bounded streams only).
sorted := kitsune.SortBy(products,
    func(p Product) float64 { return p.Price },
    func(a, b float64) bool { return a < b },
)
```

### Stream aggregation

```go
// Single-pass numeric aggregates.
total, _        := kitsune.Sum(ctx, prices)
cheapest, _, _  := kitsune.Min(ctx, prices, func(a, b float64) bool { return a < b })
priciest, _, _  := kitsune.Max(ctx, prices, func(a, b float64) bool { return a < b })
rng, _, _       := kitsune.MinMax(ctx, prices, func(a, b float64) bool { return a < b })

// MinBy / MaxBy — return the item with the extreme derived key.
oldest, _, _ := kitsune.MaxBy(ctx, users,
    func(u User) time.Time { return u.CreatedAt },
    func(a, b time.Time) bool { return a.Before(b) },
)

// Frequencies — count occurrences.
tagCounts, _ := kitsune.Frequencies(ctx, tags)
byCategory, _ := kitsune.FrequenciesBy(ctx, products,
    func(p Product) string { return p.Category },
)

// ReduceWhile — fold until a condition is met; the halting item is included.
budget := 500.0
spent, _ := kitsune.ReduceWhile(ctx, items, 0.0,
    func(acc float64, item Item) (float64, bool) {
        acc += item.Price
        return acc, acc < budget
    },
)

// TakeRandom — uniform random sample without replacement.
featured, _ := kitsune.TakeRandom(ctx, catalog, 3)
```

### Element-level transforms

```go
// WithIndex — attach position; useful for numbered output or position-aware logic.
kitsune.WithIndex(items) // → Pipeline[Pair[int, T]]

// TakeEvery / DropEvery — positional sub-sampling (0-indexed).
kitsune.TakeEvery(metrics, 10) // keep every 10th sample
kitsune.DropEvery(rows, 1)     // drop every other row

// Intersperse — insert a separator between items.
kitsune.Intersperse(fields, ",") // → f1, ",", f2, ",", f3

// ConsecutiveDedup — collapse runs of identical consecutive values.
kitsune.ConsecutiveDedup(sensorReadings)           // comparable T
kitsune.ConsecutiveDedupBy(events, func(e Event) string { return e.Type }) // by key
```


## Tails

Tails are optional extension packages that connect pipelines to external systems. Each tail is a separate Go module, so you only pull in the dependencies you use.

| Tail | Import | What |
|---|---|---|
| **kfile** | `github.com/zenbaku/go-kitsune/tails/kfile` | File, CSV, JSONL sources and sinks |
| **khttp** | `github.com/zenbaku/go-kitsune/tails/khttp` | Paginated HTTP GET source, POST/webhook sink |
| **kkafka** | `github.com/zenbaku/go-kitsune/tails/kkafka` | Kafka consumer source, producer sink |
| **kpostgres** | `github.com/zenbaku/go-kitsune/tails/kpostgres` | LISTEN/NOTIFY source, INSERT + COPY batch sink |
| **kredis** | `github.com/zenbaku/go-kitsune/tails/kredis` | Redis Store, Cache, DedupSet, list source/sink |
| **ks3** | `github.com/zenbaku/go-kitsune/tails/ks3` | S3-compatible object listing and line-streaming sources |
| **ksqlite** | `github.com/zenbaku/go-kitsune/tails/ksqlite` | SQLite query source, single/batch insert sinks |
| **kotel** | `github.com/zenbaku/go-kitsune/tails/kotel` | OpenTelemetry Hook; per-stage metrics and buffer gauges |
| **kprometheus** | `github.com/zenbaku/go-kitsune/tails/kprometheus` | Prometheus Hook; per-stage counters and duration histograms |
| **kdatadog** | `github.com/zenbaku/go-kitsune/tails/kdatadog` | Datadog DogStatsD Hook; per-stage counts and distributions |
| **knats** | `github.com/zenbaku/go-kitsune/tails/knats` | NATS core subscribe/publish + JetStream consume/publish |
| **kpubsub** | `github.com/zenbaku/go-kitsune/tails/kpubsub` | Google Cloud Pub/Sub subscribe source and publish sink |
| **ksqs** | `github.com/zenbaku/go-kitsune/tails/ksqs` | AWS SQS receive source, send sink, batch send sink |
| **kkinesis** | `github.com/zenbaku/go-kitsune/tails/kkinesis` | AWS Kinesis shard consumer source and PutRecords batch sink |
| **kdynamo** | `github.com/zenbaku/go-kitsune/tails/kdynamo` | AWS DynamoDB Scan/Query sources and BatchWriteItem sink |
| **kmongo** | `github.com/zenbaku/go-kitsune/tails/kmongo` | MongoDB Find/Watch sources and InsertMany batch sink |
| **kclickhouse** | `github.com/zenbaku/go-kitsune/tails/kclickhouse` | ClickHouse Query source and native-protocol batch Insert sink |
| **kes** | `github.com/zenbaku/go-kitsune/tails/kes` | Elasticsearch scrolling Search source and Bulk index sink |
| **kgrpc** | `github.com/zenbaku/go-kitsune/tails/kgrpc` | gRPC server-streaming source and client-streaming sink |
| **kwebsocket** | `github.com/zenbaku/go-kitsune/tails/kwebsocket` | WebSocket frame Read source and Write sink |
| **kmqtt** | `github.com/zenbaku/go-kitsune/tails/kmqtt` | MQTT Subscribe source and Publish sink |
| **kpulsar** | `github.com/zenbaku/go-kitsune/tails/kpulsar` | Apache Pulsar consumer source and producer sink |

All tails follow the **user-managed connections** principle: you create, configure, and close clients yourself. Kitsune never opens or closes connections.

See the [Tails Guide](doc/tails.md) for per-tail configuration, usage patterns, and examples.

## Runnable examples

The [`examples/`](examples/) directory contains standalone programs (root module, no extra deps):

```
examples/basic       — FromSlice, Map, Lift, ForEach
examples/filter      — Filter, Tap, Take, Drain
examples/batch       — Batch, Unbatch, BatchTimeout
examples/flatmap     — 1:N expansion patterns
examples/concurrent  — parallel workers, Ordered output, LogHook
examples/fanout      — Partition, MergeRunners
examples/errors      — Skip, Retry, RetryThen
examples/state       — MapWith, FlatMapWith, Ref
examples/compose     — Through for reusable middleware
examples/generate    — paginated APIs, tickers, infinite streams
examples/window      — sliding and tumbling windows
examples/broadcast   — Broadcast to multiple downstream runners
examples/dedupe      — Pipeline.Dedupe (MemoryDedupSet default) and Map+CacheBy
examples/iter        — FromIter with iter.Seq sources
examples/metrics     — custom Hook for per-stage metrics collection
examples/supervise   — Supervise, RestartOnError, RestartOnPanic, PanicSkip
examples/overflow    — Overflow, DropNewest, DropOldest, OverflowHook
examples/inspector   — live web dashboard: branching topology, supervision, Stop/Restart
examples/channel        — NewChannel, RunAsync: push-based source fed from a goroutine
examples/stages         — Stage[I,O], Then: composable stages tested independently
examples/timeout        — Timeout StageOption: per-item deadline, interact with Retry/Skip
examples/ticker         — Ticker, Interval: scheduled sources that emit on a regular interval
examples/pairwise       — Pairwise: consecutive overlapping pairs, compute deltas, detect direction changes
examples/concatmap      — ConcatMap: sequential ordered expansion, contrast with FlatMap
examples/slidingwindow  — SlidingWindow: rolling average, tumbling batches, sub-sampling
examples/mapresult      — MapResult: route successes and failures to separate pipelines, dead-letter queue
examples/withlatestfrom — WithLatestFrom: tag events with the latest secondary value (config, cursor position)
examples/zipwith        — ZipWith: combine two branches into a custom type without an intermediate Pair
examples/enrich         — MapBatch, LookupBy, Enrich: bulk-lookup enrichment with key deduplication
examples/streams        — Unfold, Iterate, Repeatedly, Cycle, Concat: generative and infinite sources
examples/transform      — Reject, WithIndex, Intersperse, TakeEvery, DropEvery, MapEvery, ConsecutiveDedup
examples/reshape        — ChunkBy, ChunkWhile, Sort, SortBy, Unzip: structural stream transforms
examples/aggregate      — Sum, Min, Max, MinMax, MinBy, MaxBy, Find, Frequencies, ReduceWhile, TakeRandom
examples/deadletter     — DeadLetter, DeadLetterSink: retry-embedded routing of exhausted failures
```

Run any example: `go run ./examples/basic`

Six additional examples live in their own modules (they import tail packages):

```
examples/files      — kfile CSV/JSONL sources and sinks
examples/redis      — kredis list source, Redis-backed Store and Cache
examples/sqlite     — ksqlite query source, batch insert sink
examples/http       — HTTP pagination source with retry
examples/prometheus — kprometheus hook: per-stage counters, histograms, drops, restarts
examples/websocket  — kwebsocket: Read source and Write sink over an in-process server
```

## Testing helpers

The `kitsune/testkit` package provides assertion helpers for pipeline tests:

```go
import "github.com/zenbaku/go-kitsune/testkit"

// Collect items, failing the test on error.
got := testkit.MustCollect(t, p)

// Collect and assert exact ordered output.
testkit.CollectAndExpect(t, p, []int{1, 2, 3})

// Collect and assert same elements in any order.
testkit.CollectAndExpectUnordered(t, p, []int{3, 1, 2})

// Record every lifecycle event for assertion.
hook := &testkit.RecordingHook{}
runner.Run(ctx, kitsune.WithHook(hook))
fmt.Println(hook.Items())    // per-item events
fmt.Println(hook.Errors())   // failed items
fmt.Println(hook.Restarts()) // supervision restarts
fmt.Println(hook.Graph())    // pipeline topology
```

## Docs

See [`doc/getting-started.md`](doc/getting-started.md) for a guided walkthrough: mental model, first pipeline, concurrency, error handling, branching, and testing patterns.

See [`doc/internals.md`](doc/internals.md) for the internal architecture: DAG construction, runtime compilation, channel wiring, concurrency models, and node kinds.

See [`doc/tuning.md`](doc/tuning.md) for performance tuning guidance: buffer sizing, concurrency, batching, and memory trade-offs.

See [`doc/benchmarks.md`](doc/benchmarks.md) for baseline throughput numbers, backpressure comparisons, concurrency scaling, and latency percentiles on Apple M1.

See [`doc/inspector.md`](doc/inspector.md) for the live web dashboard reference: pipeline DAG, per-stage metrics, sparklines, stop/restart controls, and dark/light theme.

See [`doc/comparison.md`](doc/comparison.md) for a comparison with other Go pipeline and streaming libraries: goroutines+channels, sourcegraph/conc, go-streams, RxGo, Watermill, Benthos, and Machinery.

## License

MIT
