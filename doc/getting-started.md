# Getting Started with Kitsune

This guide takes you from zero to a working pipeline in about 10 minutes. It covers the mental model, the key patterns you'll use daily, and where to go next.

---

## Mental model

A Kitsune pipeline is a **directed acyclic graph (DAG)** of processing stages. You assemble it by calling functions — no goroutines start, no channels are allocated. Everything is lazy. When you call `Run` (or `Collect`, or `First`), the runtime:

1. validates the graph
2. allocates bounded channels between every pair of stages
3. launches one goroutine per stage inside an errgroup
4. runs until the source is exhausted, the context is cancelled, or a stage returns an unhandled error

**Backpressure is automatic.** Each inter-stage channel has a bounded buffer (16 by default). A slow downstream stage blocks the upstream stage rather than allowing unbounded queuing.

**Context propagates everywhere.** Cancelling the context stops all stages cleanly.

### Vertical style — not fluent chains

Go's type system requires a specific code style. **Methods** preserve the element type and can be chained. **Free functions** change the type and must be assigned to a new variable:

```go
lines    := kitsune.FromSlice(rawLines)     // *Pipeline[string]
parsed   := kitsune.Map(lines, parseLog)    // *Pipeline[LogEntry]   — type changed: free function
critical := parsed.Filter(isCritical)       // *Pipeline[LogEntry]   — type preserved: method
batched  := kitsune.Batch(critical, 100)    // *Pipeline[[]LogEntry] — type changed: free function
err      := batched.ForEach(store).Run(ctx)
```

This is a Go language constraint — methods cannot introduce new type parameters — but the style is an asset: each variable name documents what's flowing, and the compiler checks every type transition.

**Rule of thumb:**
- **Free functions** (type may change, or extra type parameters required): `Map`, `FlatMap`, `Batch`, `Unbatch`, `MapWith`, `FlatMapWith`, `Reject`, `ChunkBy`, `Sort`, `SortBy`, `ZipWith`, `Unzip`, `Enrich`, …
- **Methods** (type-preserving, no extra type parameters): `.Filter`, `.Tap`, `.Take`, `.Skip`, `.Through`, `.ForEach`, `.Drain`

Not every operator fits neatly — `Reject` keeps the type but is a free function because the method form would be ambiguous with complex generics. When in doubt, look for it in both places; the [operator catalog](../README.md#operator-catalog) lists every operator with its exact call form.

---

## Your first pipeline

```go
package main

import (
    "context"
    "fmt"
    "strconv"

    kitsune "github.com/jonathan/go-kitsune"
)

func main() {
    // Source: emit each string from a slice
    input := kitsune.FromSlice([]string{"1", "2", "3", "4", "5"})

    // Transform: parse each string to int
    // kitsune.Lift wraps a context-free func(I)(O,error) for use with Map
    parsed := kitsune.Map(input, kitsune.Lift(strconv.Atoi))

    // Terminal: collect all results into a slice
    results, err := parsed.Filter(func(n int) bool { return n > 2 }).
        Collect(context.Background())
    if err != nil {
        panic(err)
    }
    fmt.Println(results) // [3 4 5]
}
```

`FromSlice` + `Collect` is the testing pattern too — deterministic, no goroutines, no infrastructure. See [`examples/basic`](../examples/basic) for the runnable version.

---

## Adding concurrency

Stage functions receive a `context.Context` as their first argument:

```go
func enrichUser(ctx context.Context, id string) (User, error) {
    return db.GetUser(ctx, id) // real I/O
}
```

To run multiple requests in parallel, add `Concurrency(n)`:

```go
users := kitsune.Map(ids, enrichUser, kitsune.Concurrency(20))
```

This starts 20 goroutines that all read from the same input channel. **Output order is not preserved** — goroutines finish in whatever order the I/O completes. If you need input order preserved in output:

```go
users := kitsune.Map(ids, enrichUser, kitsune.Concurrency(20), kitsune.Ordered())
```

`Ordered()` uses a slot-based resequencer: workers still run in parallel, but results are emitted in arrival order.

**Starting point**: 10–20 for HTTP or database calls; `runtime.NumCPU()` for CPU-bound work.

See [`examples/concurrent`](../examples/concurrent) for a runnable version with `LogHook`.

---

## Error handling

Every stage function returns `(O, error)`. By default, any error halts the entire pipeline (context cancelled, `Run` returns the error). You can change this per stage with `OnError`:

```go
// Drop the failing item and continue
parsed := kitsune.Map(lines, parseLine,
    kitsune.OnError(kitsune.Skip()),
)

// Retry up to 3 times with exponential backoff, then halt
results := kitsune.Map(queries, callAPI,
    kitsune.Concurrency(10),
    kitsune.OnError(kitsune.Retry(3, kitsune.ExponentialBackoff(time.Second, 30*time.Second))),
)

// Retry 3 times, then skip (never halt)
results := kitsune.Map(queries, callAPI,
    kitsune.OnError(kitsune.RetryThen(3,
        kitsune.ExponentialBackoff(time.Second, 30*time.Second),
        kitsune.Skip(),
    )),
)
```

For more advanced routing — send failures to a dead-letter queue instead of discarding them — use `MapResult`:

```go
ok, failed := kitsune.MapResult(items, transform)
// ok     is *Pipeline[Output]
// failed is *Pipeline[ErrItem[Input]] — contains both the original item and the error
```

See [`examples/errors`](../examples/errors) and [`examples/mapresult`](../examples/mapresult).

---

## Branching: fan-out and fan-in

**`Partition`** routes each item to one of two outputs based on a predicate:

```go
orders := kitsune.FromSlice(allOrders)
high, regular := kitsune.Partition(orders, func(o Order) bool { return o.Amount >= 100 })

vip := kitsune.Map(high, priorityProcess).ForEach(notifyVIP)
std := kitsune.Map(regular, standardProcess).ForEach(store)

// MergeRunners runs both branches — blocks until both complete
err := kitsune.MergeRunners(vip, std).Run(ctx)
```

**`Broadcast`** copies every item to all N output pipelines (unlike Partition, where each item goes to exactly one):

```go
original, audit := kitsune.Broadcast(events, 2)
```

**`Merge`** fans multiple same-type pipelines back into one:

```go
combined := kitsune.Merge(stream1, stream2, stream3)
```

See [`examples/fanout`](../examples/fanout) and [`examples/broadcast`](../examples/broadcast).

---

## Testing pipelines

Because pipelines are assembled lazily, you can test any fragment in isolation:

```go
func TestParseLine(t *testing.T) {
    input  := kitsune.FromSlice([]string{"INFO: started", "WARN: slow", "ERROR: failed"})
    result := kitsune.Map(input, parseLine, kitsune.OnError(kitsune.Skip()))

    entries, err := result.Collect(context.Background())
    require.NoError(t, err)
    require.Len(t, entries, 3)
    assert.Equal(t, "ERROR", entries[2].Level)
}
```

`FromSlice` + `Collect` is the core test pattern: no goroutines to manage, no ports to open, fully deterministic output.

For reusable pipeline fragments, define them as `Stage[I,O]` values and test each independently:

```go
var ParseStage  kitsune.Stage[string, Event]   = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[Event] { ... }
var EnrichStage kitsune.Stage[Event, Enriched] = func(p *kitsune.Pipeline[Event]) *kitsune.Pipeline[Enriched] { ... }

// Compose for production
var Pipeline = kitsune.Then(ParseStage, EnrichStage)

// Test each stage independently
events, _   := ParseStage.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
enriched, _ := EnrichStage.Apply(kitsune.FromSlice(events)).Collect(ctx)
```

See [`examples/stages`](../examples/stages) for the full stage composition and testing pattern.

---

## Where to go next

**Reference:**
- [README operator catalog](../README.md#operator-catalog) — every operator with signature and description
- [Tuning guide](tuning.md) — buffer sizing, concurrency, batching, memory trade-offs
- [Benchmarks](benchmarks.md) — throughput numbers on Apple M1

**Deeper understanding:**
- [Internals](internals.md) — DAG construction, runtime compilation, concurrency models, supervision, graceful drain

**External systems:**
- [Tails](tails.md) — connecting to Kafka, Redis, S3, Postgres, and 18 more systems

**Live observability:**
- [Inspector](inspector.md) — real-time web dashboard for running pipelines

**Runnable examples** (all in [`examples/`](../examples/)):

| Example | What it covers |
|---|---|
| `basic` | FromSlice, Map, Lift, ForEach |
| `filter` | Filter, Tap, Take, Drain |
| `batch` | Batch, Unbatch, BatchTimeout |
| `concurrent` | Concurrency, Ordered, LogHook |
| `errors` | Skip, Retry, RetryThen |
| `fanout` | Partition, MergeRunners |
| `stages` | Stage[I,O], Then, swappable sources |
| `channel` | NewChannel, RunAsync |
| `state` | MapWith, FlatMapWith, Ref |
| `supervise` | Supervise, RestartOnError, RestartOnPanic |
| `inspector` | Live web dashboard with full branching topology |
| `streams` | Unfold, Iterate, Repeatedly, Cycle, Concat — generative sources |
| `transform` | Reject, WithIndex, Intersperse, TakeEvery, DropEvery, MapEvery, ConsecutiveDedup |
| `reshape` | ChunkBy, ChunkWhile, Sort, SortBy, Unzip |
| `aggregate` | Sum, Min, Max, MinMax, MinBy, MaxBy, Find, Frequencies, ReduceWhile, TakeRandom |
| `enrich` | MapBatch, LookupBy, Enrich — bulk-fetch with key deduplication |
| `zipwith` | ZipWith — combine two branches without an intermediate Pair |
| `pairwise` | Pairwise, SlidingWindow — consecutive pair and window patterns |
| `concatmap` | ConcatMap vs FlatMap — ordered sequential expansion |
| `mapresult` | MapResult — route errors to a separate pipeline |
| `dedupe` | Dedupe, Distinct, DistinctBy, CacheBy |
| `timeout` | Timeout StageOption — per-item deadline |
| `ticker` | Ticker, Interval — scheduled sources |
| `withlatestfrom` | WithLatestFrom — combine a primary stream with the latest secondary value |
