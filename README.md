# Kitsune

A type-safe, concurrent data pipeline library for Go. Compose ordinary functions into pipelines; the runtime handles channels, goroutines, backpressure, and error routing transparently.

```go
lines  := kitsune.FromSlice(rawLines)
parsed := kitsune.Map(lines, parse)
err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
```

## Install

```
go get github.com/jonathan/go-kitsune
```

## When to use Kitsune

Kitsune is a good fit for **in-process data pipelines** where you want typed composition, backpressure, and concurrency control without boilerplate:

- **ETL and data processing** — read from files, APIs, or databases; transform; write to sinks
- **Fan-out workflows** — partition, broadcast, or duplicate streams with compile-time type safety
- **Concurrent enrichment** — call external services with bounded parallelism and automatic retry
- **Streaming aggregation** — batch, window, deduplicate, or cache in-flight data

Kitsune is **not** a distributed stream processor — there is no Kafka consumer group management, no checkpointing, and no cluster coordination. For distributed processing, look at dedicated frameworks. Kitsune complements them: use it for the in-process pipeline logic and connect to external systems through the `tails/` or your own `Generate`/`ForEach` stages.

**New to Kitsune?** Start with the [Getting Started guide](doc/getting-started.md) — mental model, first pipeline, concurrency, error handling, and testing in ~10 minutes.

## Operator catalog

**Free functions** (`Map`, `FlatMap`, `Batch`, …) change the element type as items flow through. **Methods** (`.Filter`, `.Take`, `.Skip`, …) preserve it. This split is a Go language constraint: methods cannot introduce new type parameters, so any operation that changes `Pipeline[A]` to `Pipeline[B]` must be a free function like `Map[A, B]`. The upside is that each intermediate variable documents what is flowing, and the compiler checks every type transition.

### Sources

| Function | Description |
|---|---|
| `FromSlice[T](items []T)` | Emit each element of a slice |
| `From[T](ch <-chan T)` | Wrap an existing channel (e.g., Kafka consumer) |
| `Generate[T](fn)` | Push-based custom source; call `yield` per item, return when done |
| `FromIter[T](seq iter.Seq[T])` | Wrap a Go 1.23+ iterator |
| `NewChannel[T](buffer int)` | Push-based source for external producers (HTTP handlers, event loops); see [Channel[T]](#channelt--runasync) |
| `Ticker(d)` | Emit `time.Time` on every tick of `d`; stops when context is cancelled |
| `Interval(d)` | Emit a monotonically increasing `int64` (0, 1, 2, …) on every tick of `d` |

### 1:1 Transforms

| Function | Description |
|---|---|
| `Map[I,O](p, fn, opts…)` | Apply a function to each item, potentially changing the type |
| `MapWith[I,O,S](p, key, fn, opts…)` | `Map` with injected concurrent-safe state `*Ref[S]` |
| `Map` + `CacheBy(keyFn, opts…)` | `Map` with TTL-based cache; skips `fn` on cache hit |
| `MapRecover[I,O](p, fn, recover, opts…)` | `Map` that calls `recover(ctx, item, err)` instead of failing on error |
| `MapResult[I,O](p, fn, opts…)` | Map that routes success to `ok *Pipeline[O]` and errors to `failed *Pipeline[ErrItem[I]]`; no halt or retry |

### 1:N Expansion

| Function | Description |
|---|---|
| `FlatMap[I,O](p, fn, opts…)` | Each input produces zero or more outputs |
| `ConcatMap[I,O](p, fn, opts…)` | Like `FlatMap` but always sequential (forces `Concurrency(1)`); guarantees emission order |
| `FlatMapWith[I,O,S](p, key, fn, opts…)` | `FlatMap` with injected concurrent-safe state `*Ref[S]` |
| `Pairwise[T](p)` | Emit consecutive overlapping pairs `Pair[T,T]`; first item buffers, then `{0,1},{1,2},{2,3},…` |
| `Unbatch[T](p)` | Flatten a `Pipeline[[]T]` back to individual items (inverse of `Batch`) |

### Batching & Windowing

| Function | Description |
|---|---|
| `Batch[T](p, size, opts…)` | Collect up to `size` items into a `[]T` slice; use `BatchTimeout` to flush partials |
| `Window[T](p, duration)` | Time-based batching: flush accumulated items every `duration` |
| `SlidingWindow[T](p, size, step)` | Overlapping (size > step) or tumbling (size == step) count-based windows emitted as `[]T` |

### Enrichment

Enrichment operators bulk-fetch external data for a batch of items and join it back. Keys are automatically deduplicated before each fetch call — if multiple items share the same key, only one lookup is made.

| Function | Description |
|---|---|
| `MapBatch[I,O](p, size, fn, opts…)` | Collect up to `size` items, pass the slice to `fn`, flatten results back to individual items; sugar for `Batch`+`FlatMap` |
| `LookupBy[T,K,V](p, cfg)` | Bulk-fetch a value per item using `LookupConfig.Key`/`Fetch`; emits `Pair[T,V]` — use with `ZipWith` for parallel lookups |
| `Enrich[T,K,V,O](p, cfg)` | Like `LookupBy` but calls `EnrichConfig.Join` to produce `O` directly — no intermediate `Pair` |

Config types:

```go
kitsune.LookupConfig[T, K, V]{Key, Fetch, BatchSize}
kitsune.EnrichConfig[T, K, V, O]{Key, Fetch, Join, BatchSize}
```

**Parallel lookups** — use `Broadcast` + two `LookupBy` calls + `ZipWith` to run independent fetches concurrently:

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
| `.Take(n int)` | Emit the first `n` items, then stop the pipeline |
| `.Skip(n int)` | Drop the first `n` items, emit the rest |
| `TakeWhile[T](p, fn)` | Emit while `fn` returns true; stop (and signal sources) on first false |
| `DropWhile[T](p, fn)` | Suppress while `fn` returns true; pass all items once `fn` returns false |

### Time-Based

| Function | Description |
|---|---|
| `Throttle[T](p, d)` | Emit the first item per window of `d`; drop items arriving within the cooldown |
| `Debounce[T](p, d)` | Emit only the last item after `d` of silence; each new arrival resets the timer |

### Deduplication

| Function | Description |
|---|---|
| `Distinct[T comparable](p)` | Drop duplicate items; keeps first occurrence of each value |
| `DistinctBy[T](p, key func(T) string)` | Drop duplicates by derived key; in-memory, unbounded |
| `p.Dedupe(key, set …DedupSet)` | Drop items whose key is in `set`; defaults to in-process `MemoryDedupSet` |

### Aggregation & Reduction

| Function | Description |
|---|---|
| `Scan[T,S](p, initial S, fn func(S,T) S)` | Running accumulator; emits updated state after every item |
| `Reduce[T,S](p, seed S, fn func(S,T) S)` | Fold entire stream into one value; emits once when input closes (emits `seed` on empty stream) |
| `GroupBy[T,K](p, key func(T) K)` | Collect all items into a `map[K][]T` and emit once (bounded streams only) |

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
| `Merge[T](ps…)` | Fan-in: combine multiple same-graph pipelines into one |
| `Zip[A,B](a, b)` | Pair items by position into `Pair[A,B]`; stops when the shorter input closes |
| `ZipWith[A,B,O](a, b, fn, opts…)` | Like `Zip` but applies `fn(a, b)` immediately, producing `O` directly without an intermediate `Pair` |
| `WithLatestFrom[A,B](primary, secondary)` | Combine each primary item with the most recent secondary value; drops primary items until secondary emits |

### Terminals

| Method | Returns | Description |
|---|---|---|
| `.ForEach(fn, opts…)` | `*Runner` | Process each item; call `.Run(ctx)` to execute |
| `.Drain()` | `*Runner` | Consume and discard all items |
| `runner.RunAsync(ctx, opts…)` | `<-chan error` | Start pipeline in background; channel receives one value (nil or error) |
| `.Collect(ctx, opts…)` | `([]T, error)` | Run and materialize all items into a slice |
| `.First(ctx, opts…)` | `(T, bool, error)` | Run and return the first item; `false` if stream is empty |
| `.Last(ctx, opts…)` | `(T, bool, error)` | Run and return the final item; `false` if stream is empty |
| `.Count(ctx, opts…)` | `(int64, error)` | Run and return the total number of items emitted |
| `.Any(ctx, fn, opts…)` | `(bool, error)` | Run and return `true` if any item satisfies `fn`; stops early on first match |
| `.All(ctx, fn, opts…)` | `(bool, error)` | Run and return `true` if every item satisfies `fn`; stops early on first mismatch |

### Channel[T] + RunAsync

`Channel[T]` is a push-based source for scenarios where external code drives when items arrive — HTTP handlers, CLI loops, event bridges.

```go
src   := kitsune.NewChannel[string](256)
errCh := kitsune.Map(src.Source(), parse).ForEach(store).RunAsync(ctx)

http.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
    if err := src.Send(r.Context(), readBody(r)); err != nil {
        http.Error(w, err.Error(), 500)
    }
})
src.Close()      // signal no more items (e.g. on server shutdown)
<-errCh          // wait for pipeline to drain
```

| Symbol | Description |
|---|---|
| `NewChannel[T](buffer int)` | Create a push source with the given buffer size |
| `(c) Source() *Pipeline[T]` | Wire into a pipeline; panics if called more than once |
| `(c) Send(ctx, item) error` | Block until buffer has space; returns `ErrChannelClosed` or `ctx.Err()` |
| `(c) TrySend(item) bool` | Non-blocking send; returns `false` if buffer is full or channel is closed |
| `(c) Close()` | Idempotent close; pipeline drains remaining items then exits |
| `ErrChannelClosed` | Returned by `Send` when the channel has been closed |
| `runner.RunAsync(ctx)` | Start pipeline in background goroutine; returns `<-chan error` (buffered 1) |

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
| `Then[A,B,C](first, second)` | Compose two stages into one; free function required (Go methods cannot introduce new type parameters) |

`Stage[T, T]` is directly compatible with `.Through()` — no adapter needed:

```go
var Validate kitsune.Stage[Order, Order] = func(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
    return p.Filter(isValid).Tap(logRejected)
}

orders.Through(Validate)        // existing Through API
Validate.Apply(orders)          // Stage API — identical result
kitsune.Then(Validate, enrich)  // compose with a downstream stage
```

#### Generic middleware

A Stage factory function parameterised over `T` acts as generic middleware — one definition, any item type:

```go
// WithLogging works for Pipeline[string], Pipeline[Event], Pipeline[Result], etc.
func WithLogging[T any](label string) kitsune.Stage[T, T] {
    return func(p *kitsune.Pipeline[T]) *kitsune.Pipeline[T] {
        return p.Tap(func(item T) { slog.Info(label, "item", item) })
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

The same `Stage` value runs unchanged against different sources — `FromSlice` for deterministic tests, `Channel[T]` for production:

```go
var FullPipeline = kitsune.Then(ParseStage, EnrichStage) // Stage[string, Result]

// Test — no goroutines, no timing, fully deterministic:
results, _ := FullPipeline.Apply(kitsune.FromSlice(testFixtures)).Collect(ctx)

// Production — accept pushes from HTTP handlers or event streams:
src   := kitsune.NewChannel[string](256)
errCh := FullPipeline.Apply(src.Source()).ForEach(store).RunAsync(ctx)
```

See [`examples/stages/`](examples/stages/) for a runnable version covering all four patterns.

### State

| Symbol | Description |
|---|---|
| `NewKey[T](name, initial T)` | Declare a typed, named state key with an initial value |
| `MapWith`, `FlatMapWith` | Transforms that inject a concurrent-safe `*Ref[S]` for the key |
| `Ref[T].Get(ctx)` | Read current state value |
| `Ref[T].Set(ctx, v)` | Overwrite state value |
| `Ref[T].Update(ctx, fn)` | Atomic read-modify-write |
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
| `Supervise(policy)` | Per-stage restart and panic-recovery policy |

### Error Handling

| Symbol | Description |
|---|---|
| `Halt()` | Stop pipeline on first error (default) |
| `Skip()` | Drop failing item and continue |
| `Retry(n, backoff)` | Retry up to `n` times, then halt |
| `RetryThen(n, backoff, fallback)` | Retry up to `n` times, then delegate to `fallback` |
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
| `Hook` interface | `OnStageStart`, `OnItem`, `OnStageDone` — base lifecycle events |
| `OverflowHook` | Optional extension: `OnDrop` called when items are dropped |
| `SupervisionHook` | Optional extension: `OnStageRestart` called on stage restart |
| `SampleHook` | Optional extension: `OnItemSample` called for ~every 10th item |
| `GraphHook` | Optional extension: `OnGraph` called once with the full DAG topology |
| `BufferHook` | Optional extension: `OnBuffers` called with a channel fill-level query fn |
| `LogHook(logger)` | Structured logging via `slog` |
| `MultiHook(hooks…)` | Compose multiple hooks; each sub-hook receives all events it implements |
| `WithHook(h)` | Run option: attach a hook to a pipeline run |

### Helpers & Run Options

| Symbol | Description |
|---|---|
| `Lift[I,O](fn)` | Wrap a context-free `func(I)(O,error)` for use with `Map`/`FlatMap` |
| `Pair[A,B]` | Output type of `Zip` and `WithLatestFrom`: `{First A; Second B}` |
| `ErrItem[I]` | Output type of `MapResult` failed branch: `{Item I; Err error}` |
| `MergeRunners(runners…)` | Combine forked terminal branches into a single `Runner` |
| `WithStore(s)` | Run option: set the state backend (default: `MemoryStore`) |
| `WithDrain(timeout)` | Run option: graceful shutdown — drain in-flight items before exiting |
| `WithCache(cache, ttl)` | Run option: default cache backend and TTL for all `Map`+`CacheBy` stages |

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "strconv"

    kitsune "github.com/jonathan/go-kitsune"
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

err := kitsune.MergeRunners(vip, std).Run(ctx)
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
deduped := input.Dedupe(func(e Event) string { return e.ID }, redisDedupSet)

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

## Live inspector

The `inspector` sub-package serves a real-time web dashboard that shows your pipeline graph, per-stage throughput, latency, error and drop counts, and a live sample trail.

```
go get github.com/jonathan/go-kitsune/inspector
```

### Minimal usage

```go
import (
    kitsune  "github.com/jonathan/go-kitsune"
    "github.com/jonathan/go-kitsune/inspector"
)

func main() {
    insp := inspector.New()
    defer insp.Close()
    fmt.Println("Inspector:", insp.URL()) // open in browser

    // Build your pipeline as usual.
    records := kitsune.FromSlice(rawRecords)
    parsed  := kitsune.Map(records, parse,   kitsune.WithName("parse"))
    valid   := kitsune.Map(parsed,  validate, kitsune.WithName("validate"), kitsune.Concurrency(4))

    // Pass the inspector as a hook — no other changes needed.
    err := valid.ForEach(store, kitsune.WithName("store")).Run(ctx, kitsune.WithHook(insp))
}
```

Open the printed URL in a browser. The dashboard updates in real time; click any node to open the detail sidebar.

### Stop and Restart controls

The inspector exposes UI buttons for stopping and restarting the pipeline. Wire them to your context in a run loop:

```go
insp := inspector.New()
defer insp.Close()
fmt.Println("Inspector:", insp.URL())

// Build pipeline once — Run can be called multiple times.
// ...
sink := merged.ForEach(store, kitsune.WithName("store"))

for {
    ctx, cancel := context.WithCancel(context.Background())
    cancelCh, restartCh := insp.CancelCh(), insp.RestartCh()

    // Both Stop and Restart cancel the current run.
    go func() {
        select {
        case <-cancelCh:  cancel()
        case <-restartCh: cancel()
        case <-ctx.Done():
        }
    }()

    sink.Run(ctx, kitsune.WithHook(insp))
    cancel()

    // Loop on Restart; break on Stop or any other exit.
    select {
    case <-restartCh:
        continue
    default:
    }
    break
}
```

See [`examples/inspector`](examples/inspector) for a complete branching pipeline (Partition, Broadcast, Merge, supervision, overflow) with all inspector features enabled. See [`doc/inspector.md`](doc/inspector.md) for the full dashboard reference.

## Tails

Tails are Kitsune's extension modules — optional packages that connect pipelines to external systems. Each tail is a separate Go module, so you only pull in the dependencies you use.

| Tail | Import | What |
|---|---|---|
| **kfile** | `github.com/jonathan/go-kitsune/tails/kfile` | File, CSV, JSONL sources and sinks |
| **khttp** | `github.com/jonathan/go-kitsune/tails/khttp` | Paginated HTTP GET source, POST/webhook sink |
| **kkafka** | `github.com/jonathan/go-kitsune/tails/kkafka` | Kafka consumer source, producer sink |
| **kpostgres** | `github.com/jonathan/go-kitsune/tails/kpostgres` | LISTEN/NOTIFY source, INSERT + COPY batch sink |
| **kredis** | `github.com/jonathan/go-kitsune/tails/kredis` | Redis Store, Cache, DedupSet, list source/sink |
| **ks3** | `github.com/jonathan/go-kitsune/tails/ks3` | S3-compatible object listing and line-streaming sources |
| **ksqlite** | `github.com/jonathan/go-kitsune/tails/ksqlite` | SQLite query source, single/batch insert sinks |
| **kotel** | `github.com/jonathan/go-kitsune/tails/kotel` | OpenTelemetry Hook — per-stage metrics and buffer gauges |
| **kprometheus** | `github.com/jonathan/go-kitsune/tails/kprometheus` | Prometheus Hook — counters and duration histograms per stage |
| **kdatadog** | `github.com/jonathan/go-kitsune/tails/kdatadog` | Datadog DogStatsD Hook — counts and distributions per stage |
| **knats** | `github.com/jonathan/go-kitsune/tails/knats` | NATS core subscribe/publish + JetStream consume/publish |
| **kpubsub** | `github.com/jonathan/go-kitsune/tails/kpubsub` | Google Cloud Pub/Sub subscribe source and publish sink |
| **ksqs** | `github.com/jonathan/go-kitsune/tails/ksqs` | AWS SQS receive source, send sink, batch send sink |
| **kkinesis** | `github.com/jonathan/go-kitsune/tails/kkinesis` | AWS Kinesis shard consumer source and PutRecords batch sink |
| **kdynamo** | `github.com/jonathan/go-kitsune/tails/kdynamo` | AWS DynamoDB Scan/Query sources and BatchWriteItem sink |
| **kmongo** | `github.com/jonathan/go-kitsune/tails/kmongo` | MongoDB Find/Watch sources and InsertMany batch sink |
| **kclickhouse** | `github.com/jonathan/go-kitsune/tails/kclickhouse` | ClickHouse Query source and native-protocol batch Insert sink |
| **kes** | `github.com/jonathan/go-kitsune/tails/kes` | Elasticsearch scrolling Search source and Bulk index sink |
| **kgrpc** | `github.com/jonathan/go-kitsune/tails/kgrpc` | gRPC server-streaming source and client-streaming sink |
| **kwebsocket** | `github.com/jonathan/go-kitsune/tails/kwebsocket` | WebSocket frame Read source and Write sink |
| **kmqtt** | `github.com/jonathan/go-kitsune/tails/kmqtt` | MQTT Subscribe source and Publish sink |
| **kpulsar** | `github.com/jonathan/go-kitsune/tails/kpulsar` | Apache Pulsar consumer source and producer sink |

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

## Docs

See [`doc/getting-started.md`](doc/getting-started.md) for a guided walkthrough: mental model, first pipeline, concurrency, error handling, branching, and testing patterns.

See [`doc/internals.md`](doc/internals.md) for the internal architecture: DAG construction, runtime compilation, channel wiring, concurrency models, and node kinds.

See [`doc/tuning.md`](doc/tuning.md) for performance tuning guidance: buffer sizing, concurrency, batching, and memory trade-offs.

See [`doc/benchmarks.md`](doc/benchmarks.md) for baseline throughput numbers (items/sec, allocs/op) on Apple M1.

## License

MIT
