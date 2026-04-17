# Operator Reference

This document covers every operator in go-kitsune. Each entry shows the exact Go signature, what the operator does, when to use or avoid it, which `StageOption` values apply, and a minimal working example.

**Free functions** ([`Map`](#map), [`FlatMap`](#flatmap), [`Batch`](#batch), …) can change the element type as items flow through. **Methods** ([`.Filter`](#filter), [`.Take`](#take), `.Skip`, …) preserve the type. This split is a Go generic constraint: methods cannot introduce their own type parameters, so anything that changes `Pipeline[A]` to `Pipeline[B]` must be a free function.

## :material-lightning-bolt-outline: Quick reference { #quick-ref }

Jump directly to any operator. See [Contents](#contents) for a grouped view.

| Operator | Category | Purpose |
|---|---|---|
| [`FromSlice`](#fromslice) | Source | Emit items from a Go slice |
| [`From`](#from) | Source | Wrap an existing channel |
| [`Generate`](#generate) | Source | Push-based custom generator |
| [`FromIter`](#fromiter) | Source | Wrap `iter.Seq[T]` |
| [`NewChannel`](#newchannel--channelt) | Source | Send-from-anywhere push source |
| [`Ticker`](#ticker) | Source | Periodic tick emission |
| [`Timer`](#timer) | Source | One-shot delay source |
| [`Unfold`](#unfold) | Source | Seed-based generator |
| [`Iterate`](#iterate) | Source | `x, f(x), f(f(x)), …` |
| [`Repeatedly`](#repeatedly) | Source | Repeat a function call indefinitely |
| [`Cycle`](#cycle) | Source | Loop over a fixed slice |
| [`Empty`](#empty) | Source | Complete immediately with no items |
| [`Never`](#never) | Source | Block forever (until context cancellation) |
| [`Concat`](#concat) | Source | Strictly ordered concat of pipelines |
| [`Amb`](#amb) | Source | Race factories; keep the winner |
| [`Catch`](#catch) | Source | Fallback pipeline on error |
| [`Retry`](#retry) | Source | Re-run a pipeline on failure |
| [`Using`](#using) | Source | Resource-scoped pipeline |
| [`Map`](#map) | Transform | 1:1 element transform |
| [`MapRecover`](#maprecover) | Transform | Map with panic recovery |
| [`MapResult`](#mapresult) | Transform | Map with error-branch output |
| [`DeadLetter`](#deadletter) | Transform | Retry-embedded dead-letter routing |
| [`DeadLetterSink`](#deadlettersink) | Transform | Route errored items to a sink |
| [`Timestamp`](#timestamp) | Transform | Tag each item with wall-clock time |
| [`TimeInterval`](#timeinterval) | Transform | Tag each item with elapsed duration |
| [`StartWith`](#startwith) | Transform | Prepend fixed items |
| [`DefaultIfEmpty`](#defaultifempty) | Transform | Emit a fallback if stream is empty |
| [`Intersperse`](#intersperse) | Transform | Insert separator between items |
| [`LiftPure`](#liftpure--liftfallible) | Transform | Adapt plain functions to stage signatures |
| [`Materialize`](#materialize--dematerialize) | Transform | Wrap items and terminal error as Notification values |
| [`Dematerialize`](#materialize--dematerialize) | Transform | Unwrap Notification stream back to T |
| [`FlatMap`](#flatmap) | Expansion | 1:N expansion |
| [`ConcatMap`](#concatmap) | Expansion | Sequential inner pipelines |
| [`SwitchMap`](#switchmap) | Expansion | Cancel previous inner on new item |
| [`ExhaustMap`](#exhaustmap) | Expansion | Ignore new items while inner is active |
| [`ExpandMap`](#expandmap) | Expansion | Recursive BFS expansion |
| [`Pairwise`](#pairwise) | Expansion | Emit consecutive pairs |
| [`Unbatch`](#unbatch) | Expansion | Flatten slices to individual items |
| [`Filter`](#filter) | Filter | Keep matching items |
| [`IgnoreElements`](#ignoreelements) | Filter | Drain for side effects, emit nothing |
| [`Reject`](#reject) | Filter | Drop matching items |
| [`Take`](#take) | Filter | First N items |
| [`Drop`](#drop) | Filter | Skip first N items |
| [`TakeWhile`](#takewhile) | Filter | While predicate holds |
| [`DropWhile`](#dropwhile) | Filter | Until predicate holds |
| [`TakeEvery`](#takeevery) | Filter | Keep every Nth item |
| [`DropEvery`](#dropevery) | Filter | Drop every Nth item |
| [`Distinct`](#distinct) | Filter | Global dedup by value |
| [`DistinctBy`](#distinctby) | Filter | Global dedup by key function |
| [`Dedupe`](#dedupe--dedupeby) | Filter | Consecutive or set-backed dedup |
| [`Key`](#key--newkey--ref) | Stateful | Typed state reference |
| [`MapWith`](#mapwith) | Stateful | Map with run-scoped state |
| [`FlatMapWith`](#flatmapwith) | Stateful | FlatMap with run-scoped state |
| [`MapWithKey`](#mapwithkey) | Stateful | Sharded state by key |
| [`FlatMapWithKey`](#flatmapwithkey) | Stateful | FlatMap with sharded state |
| [`Batch`](#batch) | Batch | Collect N items (or timeout) |
| [`MapBatch`](#mapbatch) | Batch | Batch → fn → flatten |
| [`Window`](#window) | Batch | Count-based tumbling window |
| [`SlidingWindow`](#slidingwindow) | Batch | Overlapping sliding windows |
| [`SessionWindow`](#sessionwindow) | Batch | Gap-based session window |
| [`ChunkBy`](#chunkby) | Batch | Consecutive same-key grouping |
| [`ChunkWhile`](#chunkwhile) | Batch | Consecutive predicate grouping |
| [`GroupByStream`](#groupbystream) | Batch | Route items to per-key sub-pipelines |
| [`Merge`](#merge) | Fan-in | N → 1 same-type streams |
| [`Partition`](#partition) | Fan-out | 1 → 2 by predicate |
| [`Broadcast`](#broadcast--broadcastn) | Fan-out | Copy to N branches |
| [`Share`](#share) | Fan-out | Hot multicast factory |
| [`Balance`](#balance) | Fan-out | Round-robin N branches |
| [`KeyedBalance`](#keyedbalance) | Fan-out | Hash-key consistent fan-out |
| [`Zip`](#zip--zipwith) | Fan-in | Pairwise combine two streams |
| [`Unzip`](#unzip) | Fan-out | Split pairs into two pipelines |
| [`LatestFrom`](#latestfrom--latestfromwith) | Fan-in | Primary + latest secondary value |
| [`CombineLatest`](#combinelatest--combinelatestwith) | Fan-in | Symmetric latest-pair emission |
| [`SampleWith`](#samplewith) | Fan-in | Emit latest on external pipeline signal |
| [`LookupBy`](#lookupby) | Enrichment | Batched key lookup |
| [`Enrich`](#enrich) | Enrichment | LookupBy + join function |
| [`Scan`](#scan) | Aggregate | Running fold, emits each step |
| [`Reduce`](#reduce) | Aggregate | Fold to single terminal result |
| [`Sum`](#sum--min--max--minmax) | Aggregate | Numeric sum |
| [`Min`](#sum--min--max--minmax) | Aggregate | Numeric minimum |
| [`Max`](#sum--min--max--minmax) | Aggregate | Numeric maximum |
| [`MinBy`](#minby--maxby) | Aggregate | Min by key function |
| [`MaxBy`](#minby--maxby) | Aggregate | Max by key function |
| [`ReduceWhile`](#reducewhile) | Aggregate | Fold until predicate fails |
| [`TakeRandom`](#takerandom) | Aggregate | Random reservoir sample |
| [`Collect`](#collect--first--last--count--any--all--find--contains) | Terminal | Return `[]T` |
| [`ToMap`](#tomap--groupby--frequencies--frequenciesby) | Terminal | Collect to map |
| [`GroupBy`](#tomap--groupby--frequencies--frequenciesby) | Terminal | Collect to `map[K][]T` |
| [`Frequencies`](#tomap--groupby--frequencies--frequenciesby) | Terminal | Count occurrences |
| [`Sort`](#sort--sortby) | Terminal | Sort and collect |
| [`Iter`](#iter) | Terminal | `iter.Seq[T]` bridge |
| [`ForEach`](#foreach) | Terminal | Run side-effect sink |
| [`Drain`](#drain) | Terminal | Consume and discard |
| [`Runner`](#runner--runasync) | Terminal | Explicit run handle |
| [`MergeRunners`](#mergerunners) | Terminal | Combine multiple runners |
| [`Throttle`](#throttle) | Time | Rate-limit (leading edge) |
| [`Debounce`](#debounce) | Time | Emit after quiet period |
| [`Sample`](#sample) | Time | Periodic latest-value emission |
| [`RateLimit`](#ratelimit) | Resilience | Token-bucket limiter |
| [`CircuitBreaker`](#circuitbreaker) | Resilience | Open/half-open/closed protection |
| [`MapPooled`](#mappooled) | Resilience | Object pool acquisition |
| [`WithIndex`](#withindex) | Utility | Tag items with position |
| [`Tap`](#tap--taperror--finally) | Utility | Side-effect on each item |
| [`TapError`](#tap--taperror--finally) | Utility | Side-effect on each error |
| [`Finally`](#tap--taperror--finally) | Utility | Side-effect on completion |
| [`Stage[I,O]`](#stagei-o--then--through--or) | Composition | Composable typed pipeline fragment |
| [`Halt`](#halt) | Error option | Stop pipeline on first error |
| [`Skip`](#skip) | Error option | Drop errored items, continue |
| [`Return`](#return) | Error option | Emit error as a value |
| [`Retry`](#retry--retrythen) | Error option | Retry with backoff |

---

## :material-table-of-contents: Contents { #contents }

1. [:material-database-import-outline: Sources](#sources)
2. [:material-swap-horizontal: 1:1 Transforms](#transforms)
3. [:material-call-split: 1:N Expansion](#expansion)
4. [:material-filter-outline: Filtering & Selection](#filtering)
5. [:material-memory: Stateful Transforms](#stateful)
6. [:material-layers-outline: Batching & Windowing](#batching)
7. [:material-source-branch: Fan-Out & Fan-In](#fan-out-fan-in)
8. [:material-database-plus-outline: Enrichment](#enrichment)
9. [:material-sigma: Aggregation & Collection](#aggregation)
10. [:material-clock-outline: Time-Based Operators](#time-based)
11. [:material-shield-check-outline: Resilience](#resilience)
12. [:material-tools: Utility & Metadata](#utility)
13. [:material-flag-checkered: Terminal Operators](#terminals)
14. [:material-puzzle-outline: Stage Composition](#stage-composition)
15. [:material-alert-circle-outline: Error Handling Options](#error-handling)
16. [:material-tune: Stage Options Reference](#options)

---

## :material-database-import-outline: Sources { #sources }

Sources are the entry point of every pipeline. They have no input pipeline; they produce items from an external data source, a collection, or a generator function.

### FromSlice

```go
func FromSlice[T any](items []T) *Pipeline[T]
```

Creates a pipeline that emits each element of `items` in order, then closes. The slice is captured by reference at construction time; do not modify it after calling `FromSlice`.

**When to use:** Tests, small fixed datasets, or any time you already have all the data in memory.

**When not to use:** Large datasets where you want to stream lazily; use [`Generate`](#generate) or [`FromIter`](#fromiter) instead.

**Options:** none (sources take no `StageOption`).

```go
nums := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
result, _ := nums.Collect(ctx)
// result == [1, 2, 3, 4, 5]
```

---

### From

```go
func From[T any](src <-chan T) *Pipeline[T]
```

Wraps an existing channel as a pipeline source. The pipeline completes when `src` is closed.

**When to use:** Bridging existing channel-based code (Kafka consumer channels, `os/signal` channels, etc.) into a kitsune pipeline.

**When not to use:** When you control the producer; [`NewChannel`](#newchannel-channelt) or [`Generate`](#generate) give you better lifecycle management.

**Options:** none.

```go
ch := make(chan Event, 64)
go kafkaConsumer(ch) // fills ch and closes it when done
p := kitsune.From(ch)
```

---

### Generate

```go
func Generate[T any](fn func(ctx context.Context, yield func(T) bool) error) *Pipeline[T]
```

Creates a push-based source from a generator function. Call `yield(item)` for each item to emit; `yield` returns `false` when the pipeline is shutting down. Return `nil` on clean completion or an error to propagate it.

`Generate` is the lowest-level source primitive; all other sources are implemented with it. The context passed to `fn` is cancelled when the pipeline shuts down, so any blocking I/O inside `fn` (long-poll RPCs, database cursors) is interrupted cleanly.

**When to use:** Paginated APIs, database cursors, WebSocket feeds, or any external source you can drive with a loop.

**Options:** none.

```go
pages := kitsune.Generate(func(ctx context.Context, yield func(Page) bool) error {
    cursor := ""
    for {
        page, next, err := api.Fetch(ctx, cursor)
        if err != nil {
            return err
        }
        if !yield(page) {
            return nil
        }
        if next == "" {
            return nil
        }
        cursor = next
    }
})
```

---

### FromIter

```go
func FromIter[T any](seq iter.Seq[T]) *Pipeline[T]
```

Wraps a Go 1.23 `iter.Seq[T]` iterator as a pipeline source. Cancellation is checked between items.

**When to use:** When you have an `iter.Seq[T]` from `slices.Values`, `maps.Keys`, or a custom iterator and want to plug it into a pipeline.

**Options:** none.

```go
import "slices"

p := kitsune.FromIter(slices.Values([]string{"a", "b", "c"}))
```

---

### NewChannel / Channel[T]

```go
func NewChannel[T any](buffer int) *Channel[T]
func (c *Channel[T]) Source() *Pipeline[T]
func (c *Channel[T]) Send(ctx context.Context, item T) error
func (c *Channel[T]) TrySend(item T) bool
func (c *Channel[T]) Close()
```

A thread-safe, push-based source for external producers. Create with `NewChannel`, obtain the pipeline with `Source()`, push items with `Send` or `TrySend`, and call `Close` when no more items will be sent. `Close` is idempotent. `Send` blocks if the buffer is full; `TrySend` returns `false` immediately instead of blocking.

**When to use:** HTTP handlers, gRPC streams, or any external goroutine that needs to feed items into a running pipeline without knowing about kitsune internals.

**Options:** none.

```go
ch := kitsune.NewChannel[Order](64)
p := ch.Source()

// In an HTTP handler (separate goroutine):
go func() {
    http.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
        var o Order
        json.NewDecoder(r.Body).Decode(&o)
        ch.Send(r.Context(), o)
    })
}()

// When the server shuts down:
ch.Close()
```

---

### Generate vs Channel[T]

Both `Generate` and `Channel[T]` bridge external code into a pipeline, but they suit different producer shapes.

| Aspect | `Generate` | `Channel[T]` |
|---|---|---|
| Producer shape | A loop you write inside `fn` | External goroutines that already exist |
| Control flow | The pipeline drives the loop; `fn` runs on the pipeline goroutine | External code drives `Send`; the pipeline only consumes |
| Backpressure | `yield` blocks when downstream is full | `Send` blocks when the buffer is full; `TrySend` returns `false` |
| Cancellation | `ctx` passed to `fn` is cancelled when the pipeline shuts down, so blocking I/O is interrupted automatically | External producers must observe their own context; `Close()` signals the pipeline to drain |
| Lifecycle | Returning from `fn` closes the source | `Close()` (idempotent) closes the source |
| Concurrency | Single goroutine (the pipeline runs `fn` once) | Safe for concurrent `Send`/`TrySend` from many goroutines |

**Choose `Generate` when** the producer is a loop you can express inline: paginated REST APIs, database cursors, polling a queue, walking a filesystem. The pipeline owns the loop and shuts it down cleanly via context cancellation.

**Choose `Channel[T]` when** items arrive from goroutines the pipeline does not own: HTTP handlers, gRPC stream handlers, library callbacks, fan-in from multiple producer goroutines. Producers stay decoupled from kitsune internals and only need a `Send` call.

If you find yourself starting a goroutine inside `Generate` just to call `yield`, use `Channel[T]` instead. If you find yourself wrapping `Channel[T]` in a single-goroutine loop, use `Generate` instead.

---

### Ticker

```go
func Ticker(d time.Duration, opts ...StageOption) *Pipeline[time.Time]
```

Emits the current `time.Time` at regular intervals. The first tick fires after `d`. The pipeline runs indefinitely until the context is cancelled or a downstream operator (like [`Take`](#take)) stops it.

**When to use:** Periodic polling, heartbeats, scheduled work triggered by time.

**Options:** `WithClock`, `WithName`.

```go
// Poll every 30 seconds; stop after 10 polls.
ticks := kitsune.Ticker(30 * time.Second).Take(10)
```

---

### Timer

```go
func Timer[T any](delay time.Duration, fn func() T, opts ...StageOption) *Pipeline[T]
```

Emits exactly one value after `delay` by calling `fn`, then closes. If the context is cancelled before `delay` elapses, no value is emitted.

**When to use:** Deferred notifications, one-shot scheduled events, timeouts that produce a sentinel value.

**Options:** `WithClock`, `WithName`.

```go
// Emit a "ping" string after 5 seconds.
ping := kitsune.Timer(5*time.Second, func() string { return "ping" })
```

---

### Unfold

```go
func Unfold[S, T any](seed S, fn func(S) (T, S, bool)) *Pipeline[T]
```

Generates a stream by repeatedly applying `fn` to an accumulator. `fn` receives the current state and returns `(value, nextState, stop)`. When `stop` is `true`, the stream ends without emitting the value.

**When to use:** Sequences derived from recurrences (Fibonacci, geometric series, tree traversals) where each step depends on the last.

**Options:** none.

```go
// Fibonacci sequence
fib := kitsune.Unfold([2]int{0, 1}, func(s [2]int) (int, [2]int, bool) {
    return s[0], [2]int{s[1], s[0] + s[1]}, false
}).Take(8)
// emits: 0, 1, 1, 2, 3, 5, 8, 13
```

---

### Iterate

```go
func Iterate[T any](seed T, fn func(T) T) *Pipeline[T]
```

Creates an infinite stream starting with `seed` where each subsequent value is produced by applying `fn` to the previous one. Use [`Take`](#take) or [`TakeWhile`](#takewhile) to bound it.

**Options:** none.

```go
kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(5)
// emits: 1, 2, 4, 8, 16
```

---

### Repeatedly

```go
func Repeatedly[T any](fn func() T) *Pipeline[T]
```

Creates an infinite stream by calling `fn` on each iteration. Use [`Take`](#take) or [`TakeWhile`](#takewhile) to bound it.

**When to use:** Sampling a random number generator, reading from a sensor, generating UUIDs.

**Options:** none.

```go
kitsune.Repeatedly(rand.Int64).Take(100)
```

---

### Cycle

```go
func Cycle[T any](items []T) *Pipeline[T]
```

Creates an infinite stream that repeatedly loops over `items`. Panics if `items` is empty.

**Options:** none.

```go
kitsune.Cycle([]string{"a", "b", "c"}).Take(7)
// emits: "a","b","c","a","b","c","a"
```

---

### Empty

```go
func Empty[T any]() *Pipeline[T]
```

Returns a Pipeline that completes immediately with no items. Useful as an identity element in pipeline composition and as a base case in tests.

`Merge(Empty[T](), p)` behaves identically to `p` for any pipeline `p`. `Amb(Empty[T](), p)` is not the same — `Amb` forwards whichever emits first, and `Empty` completes before emitting, so the winner is `p`.

**Options:** none.

```go
// Use as a no-op source in conditional pipelines:
var src *kitsune.Pipeline[Event]
if cond {
    src = realSource()
} else {
    src = kitsune.Empty[Event]()
}
```

**See also:** [`Never`](#never) (blocks indefinitely), [`FromSlice`](#fromslice) with a nil slice.

---

### Never

```go
func Never[T any]() *Pipeline[T]
```

Returns a Pipeline that never emits any items and never completes until the context is cancelled. The absorbing element for `Amb`: `Amb(Never[T](), p)` always forwards from `p`.

Useful as a placeholder in tests that assert on other branches, or to keep a merged pipeline alive while other branches are active.

**Options:** none.

```go
// In a test: ensure the other branch wins the race.
winner := kitsune.Amb(
    func() *kitsune.Pipeline[int] { return kitsune.Never[int]() },
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2, 3}) },
)
```

**See also:** [`Empty`](#empty) (completes immediately), [`Amb`](#amb).

---

### Concat

```go
func Concat[T any](factories ...func() *Pipeline[T]) *Pipeline[T]
```

Runs each pipeline factory in order: all items from factory[0] are emitted before factory[1] starts. Factories are called lazily; each one is invoked only after the previous pipeline has fully completed. Accepts factories (not `*Pipeline` values directly) so each run creates a fresh pipeline graph.

**When to use:** Sequential sources that must not overlap, such as first emitting a header, then body rows, then a footer.

**Options:** none.

```go
kitsune.Concat(
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
)
// emits: 1, 2, 3, 4
```

---

### Amb

```go
func Amb[T any](factories ...func() *Pipeline[T]) *Pipeline[T]
```

Subscribes to all factories concurrently and forwards items exclusively from whichever factory emits first, immediately cancelling all others. If no factory emits before the context is cancelled, the pipeline produces no items.

**When to use:** Hedged requests: try a primary and a replica simultaneously, use whichever responds first.

**Options:** none.

```go
result := kitsune.Amb(
    func() *kitsune.Pipeline[Result] { return fetchFromPrimary(ctx) },
    func() *kitsune.Pipeline[Result] { return fetchFromReplica(ctx) },
)
```

---

### Catch

```go
func Catch[T any](p *Pipeline[T], fn func(error) *Pipeline[T]) *Pipeline[T]
```

Runs `p` normally. If `p` returns a non-nil, non-context error, `fn` is called with the error and the returned fallback pipeline is subscribed; items already emitted by `p` are kept and the fallback's items follow. If `p` completes without error, the fallback is never started.

**When to use:** Streaming fallback to a secondary source when the primary fails.

**Options:** none.

```go
kitsune.Catch(primaryFeed, func(err error) *kitsune.Pipeline[Event] {
    log.Printf("primary failed (%v); switching to backup", err)
    return backupFeed()
})
```

---

### Retry

```go
func Retry[T any](p *Pipeline[T], pol RetryPolicy) *Pipeline[T]
```

Re-runs the entire pipeline `p` from scratch whenever it errors, according to `pol`. This is the right primitive for sources that must reconnect on failure: websocket tails, change-data-capture streams, long-poll HTTP. The correct response to a disconnect is to re-establish the connection and resume.

Items produced during any attempt (including partial output from a failed attempt) are forwarded downstream immediately; `Retry` does not buffer or replay. Downstream observes the concatenation of each attempt's output.

**When to use:** Persistent reconnect-on-drop semantics for a source pipeline. For per-item retries of a transformation function, use `OnError(RetryMax(...))` instead.

**Options:** none. Configure behaviour via the `RetryPolicy` argument.

**`RetryPolicy` fields:**

| Field | Type | Description |
|---|---|---|
| `MaxAttempts` | `int` | Total runs allowed including the first. `≤ 0` means unlimited. |
| `Backoff` | `Backoff` | Wait duration before the Nth retry (0-indexed). `nil` = no delay. |
| `Retryable` | `func(error) bool` | Which errors are eligible for retry. `nil` = all non-context errors. |
| `OnRetry` | `func(attempt int, err error, wait time.Duration)` | Called before each sleep; useful for logging. |

**Convenience constructors:**

```go
kitsune.RetryUpTo(n, backoff)   // at most n total attempts
kitsune.RetryForever(backoff)   // retry until context cancellation
```

```go
// Reconnecting websocket tail: retry forever with exponential backoff.
kitsune.Retry(
    kitsune.Generate(websocketTail),
    kitsune.RetryForever(kitsune.ExponentialBackoff(100*time.Millisecond, 30*time.Second)),
)

// Retry up to 5 times, only on transient network errors.
kitsune.Retry(
    primaryFeed,
    kitsune.RetryUpTo(5, kitsune.FixedBackoff(time.Second)).
        WithRetryable(func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }).
        WithOnRetry(func(attempt int, err error, _ time.Duration) {
            log.Printf("retry %d after: %v", attempt+1, err)
        }),
)
```

---

### Using

```go
func Using[T, R any](
    acquire func(context.Context) (R, error),
    build func(R) *Pipeline[T],
    release func(R),
) *Pipeline[T]
```

Acquires a resource, builds a pipeline from it, and guarantees `release` is called exactly once when the pipeline exits, regardless of success, error, or cancellation. If `acquire` returns an error, no items are emitted and `release` is not called.

**When to use:** Database connections, file handles, or any resource that must be explicitly released when the pipeline finishes.

**Options:** none.

```go
p := kitsune.Using(
    func(ctx context.Context) (*sql.Rows, error) {
        return db.QueryContext(ctx, "SELECT id, name FROM users")
    },
    func(rows *sql.Rows) *kitsune.Pipeline[User] {
        return kitsune.Generate(func(ctx context.Context, yield func(User) bool) error {
            for rows.Next() {
                var u User
                if err := rows.Scan(&u.ID, &u.Name); err != nil {
                    return err
                }
                if !yield(u) {
                    return nil
                }
            }
            return rows.Err()
        })
    },
    func(rows *sql.Rows) { rows.Close() },
)
```

---

## :material-swap-horizontal: 1:1 Transforms { #transforms }

Each item in produces exactly one item out, potentially of a different type.

### Map

```go
func Map[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[O]
```

Applies `fn` to each item, potentially changing the element type from `I` to `O`. The context passed to `fn` is cancelled if the pipeline shuts down; always use it for any I/O calls inside `fn`.

With `Concurrency(n)` > 1, `fn` is called from `n` goroutines in parallel. Results arrive in completion order unless you add `Ordered()`. The engine has a serial fast path that activates when concurrency is 1, there is no error handler override, and no hook; this path avoids all overhead per item.

**When to use:** Any 1:1 transformation: JSON parsing, struct conversion, external API calls.

**When not to use:** When you need to emit zero or multiple outputs per item; use [`Filter`](#filter) or [`FlatMap`](#flatmap).

**Options:** `Concurrency`, `Ordered`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`, `CacheBy`.

```go
// Parse log lines concurrently, preserving order.
parsed := kitsune.Map(lines, func(ctx context.Context, line string) (LogEntry, error) {
    return parseLine(line)
}, kitsune.Concurrency(8), kitsune.Ordered())
```

---

### MapRecover

```go
func MapRecover[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I) (O, error),
    recover func(context.Context, I, error) O,
    opts ...StageOption,
) *Pipeline[O]
```

Applies `fn` to each item. If `fn` returns an error or panics, `recover` is called with the original item and the error to produce a fallback output value. The output pipeline always emits exactly one item per input; no items are dropped and the pipeline never halts on a per-item failure.

**When to use:** When you want a guaranteed-1:1 transform that substitutes a sentinel or default on failure, rather than propagating the error or dropping the item.

**Options:** `Buffer`, `Timeout`, `WithName`.

```go
enriched := kitsune.MapRecover(events,
    func(ctx context.Context, e Event) (EnrichedEvent, error) {
        return enrich(ctx, e)
    },
    func(ctx context.Context, e Event, err error) EnrichedEvent {
        log.Printf("enrich failed for %v: %v", e.ID, err)
        return EnrichedEvent{Event: e, Enriched: false}
    },
)
```

---

### MapResult

```go
func MapResult[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I) (O, error),
    opts ...StageOption,
) (*Pipeline[O], *Pipeline[ErrItem[I]])
```

Applies `fn` to each item and routes by outcome: successful outputs go to the first (`ok`) pipeline; failures go to the second (`failed`) pipeline as `ErrItem[I]` values containing the original input and the error. The pipeline never halts. Both output pipelines must be consumed.

`ErrItem[I]` is defined as:

```go
type ErrItem[I any] struct {
    Item I
    Err  error
}
```

Unlike [`DeadLetter`](#deadletter), `MapResult` does not retry; every failure routes to the error pipeline immediately. Unlike `OnError(Skip())`, failed items are not silently discarded; they are available for inspection, logging, or dead-letter storage.

**When to use:** When you want to process failures separately without halting the pipeline: audit trails, error reporting pipelines, reprocessing queues.

**Options:** `Buffer`, `Timeout`, `WithName`.

```go
ok, failed := kitsune.MapResult(records, func(ctx context.Context, r Record) (Stored, error) {
    return db.Store(ctx, r)
})

stored := ok.ForEach(func(_ context.Context, s Stored) error {
    metrics.Inc("stored")
    return nil
}).Build()

logged := failed.ForEach(func(_ context.Context, e kitsune.ErrItem[Record]) error {
    log.Printf("failed to store %v: %v", e.Item.ID, e.Err)
    return nil
}).Build()

runner, _ := kitsune.MergeRunners(stored, logged)
runner.Run(ctx)
```

---

### DeadLetter

```go
func DeadLetter[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I) (O, error),
    opts ...StageOption,
) (*Pipeline[O], *Pipeline[ErrItem[I]])
```

Like [`MapResult`](#mapresult) but with retry embedded. Use `OnError(Retry(...))` in `opts` to configure the retry policy. Items that succeed on any attempt go to the `ok` pipeline; items that exhaust all retries go to the `dlq` pipeline as `ErrItem[I]`. Both pipelines must be consumed.

**When to use:** External calls where transient errors should be retried, with permanent failures routed to a dead-letter queue for later inspection or reprocessing.

**Options:** `OnError` (with `Retry` or `RetryThen`), `Buffer`, `Timeout`, `WithName`.

```go
ok, dlq := kitsune.DeadLetter(orders, func(ctx context.Context, o Order) (Receipt, error) {
    return paymentAPI.Charge(ctx, o)
}, kitsune.OnError(
    kitsune.RetryMax(3, kitsune.ExponentialBackoff(100*time.Millisecond, 5*time.Second)),
))

receipts := ok.ForEach(saveReceipt).Build()
failures := dlq.ForEach(func(_ context.Context, e kitsune.ErrItem[Order]) error {
    return dlqQueue.Publish(ctx, e)
}).Build()

runner, _ := kitsune.MergeRunners(receipts, failures)
runner.Run(ctx)
```

---

### DeadLetterSink

```go
func DeadLetterSink[I any](
    p *Pipeline[I],
    fn func(context.Context, I) error,
    opts ...StageOption,
) (*Pipeline[ErrItem[I]], *Runner)
```

Like [`DeadLetter`](#deadletter) but for terminal sinks (where `fn` produces no output). Returns a dead-letter pipeline and a `Runner`. The dead-letter pipeline must be consumed before calling `runner.Run`.

**Options:** `OnError` (with `Retry`), `Concurrency`, `Buffer`, `Timeout`, `WithName`.

```go
dlq, runner := kitsune.DeadLetterSink(events, func(ctx context.Context, e Event) error {
    return db.Write(ctx, e)
}, kitsune.OnError(kitsune.RetryMax(3, kitsune.FixedBackoff(50*time.Millisecond))))

_ = dlq.ForEach(func(_ context.Context, e kitsune.ErrItem[Event]) error {
    return failureLog.Append(ctx, e)
}).Build()

runner.Run(ctx)
```

---

### Timestamp

```go
func Timestamp[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Timestamped[T]]
```

Tags each item with the wall-clock time it was observed at this stage. Emits `Timestamped[T]{Value T; Time time.Time}`.

**Options:** `WithClock`, `WithName`, `Buffer`.

```go
ts := kitsune.Timestamp(events)
// emits: Timestamped[Event]{Value: e, Time: <now>}
```

---

### TimeInterval

```go
func TimeInterval[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[TimedInterval[T]]
```

Tags each item with the elapsed duration since the previous item. The first item always has `Elapsed == 0`. Emits `TimedInterval[T]{Value T; Elapsed time.Duration}`. Always runs at `Concurrency(1)`.

**Options:** `WithClock`, `WithName`, `Buffer`.

```go
intervals := kitsune.TimeInterval(events)
// use intervals to detect stalls: if Elapsed > threshold, alert
```

---

### StartWith

```go
func StartWith[T any](p *Pipeline[T], items ...T) *Pipeline[T]
```

Prepends one or more items before the first item from `p`. The prefix is always emitted in full before `p` begins.

**Options:** none.

```go
withHeader := kitsune.StartWith(rows, headerRow)
```

---

### DefaultIfEmpty

```go
func DefaultIfEmpty[T any](p *Pipeline[T], defaultVal T, opts ...StageOption) *Pipeline[T]
```

Forwards all items from `p` unchanged. If `p` completes without emitting any items, emits `defaultVal` once.

**Options:** `Buffer`, `WithName`.

```go
result := kitsune.DefaultIfEmpty(searchResults, noResultsSentinel)
```

---

### Intersperse

```go
func Intersperse[T any](p *Pipeline[T], sep T, opts ...StageOption) *Pipeline[T]
```

Inserts `sep` between consecutive items. The separator is never emitted at the start or end of the stream.

**Options:** `Buffer`, `WithName`.

```go
// 1,2,3 → 1,0,2,0,3
kitsune.Intersperse(nums, 0)
```

---

### LiftPure / LiftFallible

```go
func LiftPure[I, O any](fn func(I) O) func(context.Context, I) (O, error)
func LiftFallible[I, O any](fn func(I) (O, error)) func(context.Context, I) (O, error)
```

Adapter helpers that wrap a context-free or context-free-fallible function into the signature expected by [`Map`](#map), [`Filter`](#filter), etc.

```go
doubled := kitsune.Map(p, kitsune.LiftPure(func(n int) int { return n * 2 }))
```

---

### Materialize / Dematerialize

```go
func Materialize[T any](p *Pipeline[T]) *Pipeline[Notification[T]]
func Dematerialize[T any](p *Pipeline[Notification[T]], opts ...StageOption) *Pipeline[T]
```

`Materialize` converts each item and the terminal outcome of `p` into a `Notification[T]` value emitted on a single output pipeline. `Dematerialize` is the inverse: it unwraps a `Notification[T]` stream back into a plain `T` stream.

`Notification[T]` is a sum type:

```go
type Notification[T any] struct {
    Value T
    Err   error
    Done  bool
}
```

Helper constructors and predicates are provided:

| Constructor | `IsValue()` | `IsError()` | `IsComplete()` |
|---|---|---|---|
| `NextNotification(v)` | ✓ | | |
| `ErrorNotification(err)` | | ✓ | |
| `CompleteNotification[T]()` | | | ✓ |

**Materialize emission rules:**

| Upstream event | Emitted notification | `Run` result |
|---|---|---|
| Item `v` | `NextNotification(v)` | — |
| Normal completion | `CompleteNotification[T]()` | `nil` |
| Pipeline error `err` | `ErrorNotification[T](err)` | `nil` |
| Context cancellation | *(none)* | `ctx.Err()` |

The key property: `Materialize` never propagates pipeline errors. They are encoded as the final notification instead, so downstream operators continue running. Context cancellation is not materialized; it exits the run immediately.

**Dematerialize processing rules:**

| Notification | Action |
|---|---|
| `IsValue()` | Emit `n.Value` downstream |
| `IsComplete()` | Complete normally |
| `IsError()` | Re-inject `n.Err` as a pipeline error |
| Upstream closed without terminal | Complete normally (defensive) |

**When to use:** When you need to pass error events through operators that only handle `T` — for example, routing, logging, or filtering errors without halting the pipeline. The standard pattern:

```go
// Classify notifications by outcome without halting on errors.
classified := kitsune.Map(
    kitsune.Materialize(src),
    func(_ context.Context, n kitsune.Notification[Event]) (TaggedEvent, error) {
        if n.IsError() {
            return TaggedEvent{Err: n.Err, Source: "pipeline"}, nil
        }
        if n.IsComplete() {
            return TaggedEvent{Done: true}, nil
        }
        return TaggedEvent{Event: n.Value}, nil
    },
)
```

**Options (`Dematerialize` only):** `Buffer`, `Overflow`, `WithName`.

**Options (`Materialize`):** none — it wraps the upstream run internally and cannot accept per-stage options.

**See also:** [`MapResult`](#mapresult) (routes errors to a separate pipeline without materialization), [`TapError`](#tap--taperror--finally) (side-effect on terminal error), [`Catch`](#catch) (fallback pipeline on error).

---

## :material-call-split: 1:N Expansion { #expansion }

These operators allow each input item to produce zero or more output items.

### FlatMap

```go
func FlatMap[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Transforms each input into zero or more outputs. `fn` calls `yield(item)` for each output it wants to emit; `yield` returns an error if the pipeline is shutting down. With `Concurrency(n)` > 1, multiple input items are processed in parallel. With `Ordered()`, all outputs from item `i` are emitted before any outputs from item `i+1`.

**When to use:** Expanding records into sub-records, fetching related items per input, or flattening nested structures.

**Options:** `Concurrency`, `Ordered`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

```go
// Expand each user into their orders.
orders := kitsune.FlatMap(users,
    func(ctx context.Context, u User, yield func(Order) error) error {
        orders, err := db.OrdersForUser(ctx, u.ID)
        if err != nil {
            return err
        }
        for _, o := range orders {
            if err := yield(o); err != nil {
                return err
            }
        }
        return nil
    }, kitsune.Concurrency(4))
```

---

### ConcatMap

```go
func ConcatMap[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Like [`FlatMap`](#flatmap) but always processes items sequentially; the next item starts only after the current item's inner pipeline has fully emitted. Output order is fully preserved. This is equivalent to [`FlatMap`](#flatmap) with `Concurrency(1)`, but the intent is made explicit.

**When to use:** When you need strictly ordered output or when sub-streams have side effects that must not overlap.

**Options:** `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

`ConcatMap` is always serial. Passing `Concurrency(n)` with `n > 1` panics at pipeline construction time; use [`FlatMap`](#flatmap) with `Concurrency(n)` if you want parallel fan-out.

```go
// Each file is processed completely before the next starts.
lines := kitsune.ConcatMap(filePaths,
    func(ctx context.Context, path string, yield func(string) bool) error {
        f, err := os.Open(path)
        if err != nil {
            return err
        }
        defer f.Close()
        scanner := bufio.NewScanner(f)
        for scanner.Scan() {
            if err := yield(scanner.Text()); err != nil {
                return err
            }
        }
        return scanner.Err()
    })
```

---

### SwitchMap

```go
func SwitchMap[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Transforms each input into a sub-stream. When a new input arrives, the currently running sub-stream is cancelled immediately and the new one begins. Only the most-recently-started sub-stream's outputs reach downstream; older sub-streams are abandoned even if they have not finished emitting.

**When to use:** Typeahead search (cancel the previous request when the user types again), "latest wins" streaming; only the most recent input matters.

**When not to use:** When you need all outputs from all inputs, or when sub-streams have committed side effects that cannot be safely cancelled mid-flight.

**Options:** `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

```go
// For each search query, fetch results; cancel old search when new query arrives.
results := kitsune.SwitchMap(queries,
    func(ctx context.Context, q string, yield func(Result) error) error {
        hits, err := search.Query(ctx, q)
        if err != nil {
            return err
        }
        for _, h := range hits {
            if err := yield(h); err != nil {
                return err
            }
        }
        return nil
    })
```

---

### ExhaustMap

```go
func ExhaustMap[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Transforms each input into a sub-stream, but while a sub-stream is in progress, new input items are silently dropped. Only when the current sub-stream finishes is the next item accepted. The opposite of [`SwitchMap`](#switchmap), using "first wins" rather than "latest wins".

**When to use:** Rate-limited refresh operations where you want to ignore duplicate triggers while one is already running, e.g., a cache refresh that should not run concurrently with itself.

**Options:** `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

```go
// Refresh cache on signal; ignore duplicate signals while refresh is in progress.
refreshed := kitsune.ExhaustMap(signals,
    func(ctx context.Context, _ Signal, yield func(CacheSnapshot) error) error {
        snap, err := rebuildCache(ctx)
        if err != nil {
            return err
        }
        return yield(snap)
    })
```

---

### ExpandMap

```go
func ExpandMap[T any](
    p *Pipeline[T],
    fn func(context.Context, T) *Pipeline[T],
    opts ...StageOption,
) *Pipeline[T]
```

BFS graph expansion. For each item, `fn` returns a child pipeline (or `nil` for leaf items). Items are emitted in BFS order. Use `VisitedBy` to detect and skip cycles.

**When to use:** Tree or DAG traversal where each node can produce more nodes of the same type: directory trees, dependency graphs, org charts.

**Options:** `Buffer`, `WithName`, `MaxDepth`, `MaxItems`, `VisitedBy` (for cycle detection), `WithDedupSet`.

> **Warning — unbounded by default.** Without `MaxDepth`, `MaxItems`, or a downstream `Take(n)`, `ExpandMap` will traverse the entire reachable graph. A graph with branching factor `fan` and depth `d` produces up to `fan^d` items, which can exhaust memory silently as the BFS queue grows. Always bound expansion on untrusted or potentially deep inputs.

```go
// Crawl a directory tree.
files := kitsune.ExpandMap(
    kitsune.FromSlice([]string{"/root"}),
    func(ctx context.Context, path string) *kitsune.Pipeline[string] {
        entries, err := os.ReadDir(path)
        if err != nil {
            return nil
        }
        var children []string
        for _, e := range entries {
            if e.IsDir() {
                children = append(children, filepath.Join(path, e.Name()))
            }
        }
        return kitsune.FromSlice(children)
    },
)
```

Bounded expansion — cap both depth and total entries:

```go
// Walk at most 4 levels deep and at most 10 000 entries total.
files := kitsune.ExpandMap(
    kitsune.FromSlice([]string{"/root"}),
    func(ctx context.Context, path string) *kitsune.Pipeline[string] {
        entries, err := os.ReadDir(path)
        if err != nil {
            return nil
        }
        var children []string
        for _, e := range entries {
            if e.IsDir() {
                children = append(children, filepath.Join(path, e.Name()))
            }
        }
        return kitsune.FromSlice(children)
    },
    kitsune.MaxDepth(4),
    kitsune.MaxItems(10_000),
)
```

When either bound is reached the stage stops enqueueing children and closes its output channel normally — no error is returned, matching the semantics of `Take(n)`. If both options are set, whichever limit fires first wins.

---

### Pairwise

```go
func Pairwise[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Consecutive[T]]
```

Emits overlapping consecutive pairs: `{item[0], item[1]}`, `{item[1], item[2]}`, `{item[2], item[3]}`, …. The first item is held internally; no pair is emitted until the second item arrives. A stream of `n` items produces `n-1` pairs.

**When to use:** Computing deltas between consecutive values, detecting direction changes, change detection.

**Options:** `Buffer`, `WithName`.

```go
deltas := kitsune.Map(
    kitsune.Pairwise(prices),
    kitsune.LiftPure(func(c kitsune.Consecutive[float64]) float64 {
        return c.Curr - c.Prev
    }),
)
```

---

### Unbatch

```go
func Unbatch[T any](p *Pipeline[[]T], opts ...StageOption) *Pipeline[T]
```

Flattens a pipeline of slices into a pipeline of individual items. This is the inverse of [`Batch`](#batch).

**When to use:** When you receive data in batches (bulk API response, database rows) and want to process items individually downstream.

**Options:** `Buffer`, `WithName`.

```go
// API returns batches; process each item individually.
items := kitsune.Unbatch(kitsune.Map(pages, fetchPage))
```

---

## :material-filter-outline: Filtering & Selection { #filtering }

### Filter

```go
func Filter[T any](p *Pipeline[T], pred func(context.Context, T) (bool, error), opts ...StageOption) *Pipeline[T]
```

Emits only items for which `pred` returns `true`. Items for which `pred` returns `false` are silently dropped. Items for which `pred` returns an error halt the pipeline (unless `OnError` is set).

The method form on `*Pipeline[T]` accepts a simpler `func(T) bool`:

```go
func (p *Pipeline[T]) Filter(fn func(T) bool, opts ...StageOption) *Pipeline[T]
```

**Options (free function):** `Buffer`, `Overflow`, `WithName`. `OnError` applies if the predicate can return errors.

```go
// Free function with context-aware predicate:
active := kitsune.Filter(users, func(ctx context.Context, u User) (bool, error) {
    return subscriptionDB.IsActive(ctx, u.ID)
})

// Method form with simple predicate:
adults := users.Filter(func(u User) bool { return u.Age >= 18 })
```

---

### IgnoreElements

```go
func IgnoreElements[T any](p *Pipeline[T]) *Pipeline[T]
```

Drains `p` for its side effects and emits nothing downstream. The returned pipeline completes (or errors) when `p` completes (or errors). Any [`Tap`](#tap--taperror--finally), [`Map`](#map), or other side-effecting operators in `p` still run.

Also available as a method: `p.IgnoreElements()`.

**When to use:** You want a pipeline to run for its side effects (writes, metrics, logging) without forwarding any items to downstream consumers.

**Options:** none.

```go
// Run a Tap-instrumented pipeline for its side effects only:
kitsune.IgnoreElements(
    kitsune.Tap(events, func(_ context.Context, e Event) error {
        metrics.Record(e)
        return nil
    }),
).Run(ctx)

// Method form:
events.Tap(metrics.Record).IgnoreElements().Run(ctx)
```

**See also:** [`ForEach`](#foreach) (terminal; use when you own the run), [`Filter`](#filter) (keeps matching items).

---

### Reject

```go
func Reject[T any](p *Pipeline[T], pred func(context.Context, T) (bool, error), opts ...StageOption) *Pipeline[T]
```

The inverse of [`Filter`](#filter). Emits only items for which `pred` returns `false` (discards items where `pred` is `true`). Also available as a method with `func(T) bool`.

```go
nonEmpty := strings.Reject(func(s string) bool { return s == "" })
```

---

### Take

```go
func Take[T any](p *Pipeline[T], n int) *Pipeline[T]
```

Emits the first `n` items and then stops the pipeline, signalling upstream sources to stop producing. Infinite sources like [`Ticker`](#ticker) and [`Repeatedly`](#repeatedly) stop cleanly when `Take` closes.

Also available as `p.Take(n)`.

**Options:** none.

```go
first10 := kitsune.Take(events, 10)
```

---

### Drop

```go
func Drop[T any](p *Pipeline[T], n int) *Pipeline[T]
```

Discards the first `n` items, then forwards all subsequent items unchanged.

Also available as `p.Drop(n)` and `p.Skip(n)` (alias).

**Options:** none.

```go
// Skip the header row.
data := kitsune.Drop(csvLines, 1)
```

---

### TakeWhile

```go
func TakeWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T]
```

Emits items as long as `pred` returns `true`. As soon as `pred` returns `false`, the pipeline stops; the item that failed the predicate is not emitted. Unlike [`Filter`](#filter), which drops individual items, `TakeWhile` terminates the pipeline.

**Options:** none.

```go
// Stop reading when we reach a sentinel record.
data := kitsune.TakeWhile(records, func(r Record) bool { return r.Type != "EOF" })
```

---

### DropWhile

```go
func DropWhile[T any](p *Pipeline[T], pred func(T) bool) *Pipeline[T]
```

Discards items as long as `pred` returns `true`. The first item for which `pred` returns `false` (and all subsequent items) are forwarded.

**Options:** none.

```go
// Skip header lines starting with '#'.
lines := kitsune.DropWhile(rawLines, func(s string) bool { return strings.HasPrefix(s, "#") })
```

---

### TakeEvery

```go
func TakeEvery[T any](p *Pipeline[T], n int) *Pipeline[T]
```

Emits every `n`th item starting with the first (index 0). Items at indices 1, 2, …, n-1 are dropped; the item at index n is emitted; and so on. Panics if `n <= 0`.

**Options:** none.

```go
// Sample every 10th reading.
sampled := kitsune.TakeEvery(sensorReadings, 10)
```

---

### DropEvery

```go
func DropEvery[T any](p *Pipeline[T], n int) *Pipeline[T]
```

Drops every `n`th item (indices 0, n, 2n, …), forwarding all others. Panics if `n <= 0`.

**Options:** none.

```go
// Drop every 5th item.
filtered := kitsune.DropEvery(events, 5)
```

---

### Distinct

```go
func Distinct[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T]
```

Emits only items that have not been seen before in the entire stream, using `==` equality. Keeps an in-memory set of all seen values; memory usage grows with the number of unique items.

**Options:** `WithDedupSet` (to use a Redis or Bloom filter backend), `Buffer`, `WithName`.

```go
uniqueIDs := kitsune.Distinct(allIDs)
```

---

### DistinctBy

```go
func DistinctBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T]
```

Like [`Distinct`](#distinct) but uses `keyFn` to derive the comparison key, allowing deduplication of complex types by a single field.

**Options:** `WithDedupSet`, `Buffer`, `WithName`.

```go
// Deduplicate events by their ID field.
unique := kitsune.DistinctBy(events, func(e Event) string { return e.ID })
```

---

### Dedupe / DedupeBy

```go
func Dedupe[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T]
func DedupeBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T]
```

Drops consecutive duplicate items (adjacent-only deduplication). Unlike [`Distinct`](#distinct), non-adjacent duplicates are not suppressed. `Dedupe` uses `==` equality; `DedupeBy` uses a key function.

If a `WithDedupSet` backend is provided, deduplication becomes global (not just consecutive); any item whose key was seen anywhere in the stream is dropped.

The method form `p.Dedupe(keyFn)` uses a `MemoryDedupSet` by default for global deduplication.

**Options:** `WithDedupSet`, `Buffer`, `WithName`.

```go
// Suppress consecutive duplicate status updates.
changes := kitsune.DedupeBy(statusUpdates, func(s Status) string { return s.State })
```

---

### DedupSet backends

`Distinct`, `DistinctBy`, `Dedupe`, `DedupeBy`, and `ExpandMap` all accept a `WithDedupSet(backend)` option to override their default in-process deduplication store. Three built-in backends are provided:

#### MemoryDedupSet

```go
func MemoryDedupSet() DedupSet
```

Unbounded in-memory set. Never evicts. Suitable for finite streams or when the key space is bounded. The default for all dedup operators.

#### BloomDedupSet

```go
func BloomDedupSet(expectedItems int, falsePositiveRate float64) DedupSet
```

Bounded probabilistic set backed by a Bloom filter. Memory usage is fixed regardless of key-space size; items are never missed (zero false-negative rate), but a configured false-positive rate allows a small fraction of unseen keys to appear seen. Panics if `expectedItems <= 0` or `falsePositiveRate` is not in `(0, 1)`.

**When to use:** when the key space is unbounded but bounded memory is required and occasional false positives are acceptable (e.g. spam suppression, cache-miss avoidance).

#### TTLDedupSet

```go
func TTLDedupSet(ttl time.Duration) DedupSet
```

In-process deduplication set that forgets keys `ttl` after they were last added. Memory is bounded by the set of currently non-expired keys. Eviction is lazy: expired entries are purged on the next `Contains` or `Add` call; there is no background goroutine. Re-adding an existing key refreshes its expiry (touch semantics). Panics if `ttl <= 0`.

**When to use:** deduplicating a high-volume event stream over a sliding time window (e.g. suppress duplicate webhooks received within the last 5 minutes) without unbounded memory growth. Prefer over `MemoryDedupSet` when keys must be forgotten; prefer over `BloomDedupSet` when zero false positives are required.

```go
// Suppress duplicate webhook deliveries within a 5-minute window.
set := kitsune.TTLDedupSet(5 * time.Minute)
unique := kitsune.DistinctBy(events, func(e Event) string { return e.ID },
    kitsune.WithDedupSet(set),
)
```

---

## :material-memory: Stateful Transforms { #stateful }

These operators inject a `*Ref[S]`, a concurrent-safe state handle, into the stage function, enabling accumulators, counters, and running state across items.

### Key / NewKey / Ref

```go
func NewKey[T any](name string, initial T, opts ...KeyOption) Key[T]
```

Declares a typed, run-scoped state key. Declare keys as package-level variables. The `initial` value is used at the start of each `runner.Run`. Use `StateTTL(d)` to expire state after a period of inactivity.

```go
var callCountKey = kitsune.NewKey[int]("call_count", 0)
```

A `Ref[T]` injected by [`MapWith`](#mapwith) provides:

- `Get(ctx)`: read current value
- `Set(ctx, value)`: overwrite
- `Update(ctx, fn)`: atomic read-modify-write
- `UpdateAndGet(ctx, fn)`: atomic read-modify-write, returns new value
- `GetOrSet(ctx, fn)`: return existing or initialise

---

### MapWith

```go
func MapWith[I, O, S any](
    p *Pipeline[I],
    key Key[S],
    fn func(context.Context, *Ref[S], I) (O, error),
    opts ...StageOption,
) *Pipeline[O]
```

Like [`Map`](#map) but injects a `*Ref[S]` carrying persistent state identified by `key`. At `Concurrency(1)` (the default), a single `Ref` is shared across all items in sequence; this is perfect for running totals, accumulators, or event counters. With `Concurrency(n)` > 1, each worker goroutine gets its own independent `Ref` (worker-local state).

State survives across items within a single `runner.Run`. Use `WithStore` at run time to persist state to Redis, DynamoDB, etc.

**When to use:** Running counters, sequence numbering, de-duplication with memory, rate tracking per pipeline run.

**Options:** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

```go
var seqKey = kitsune.NewKey[int]("seq", 0)

numbered := kitsune.MapWith(events, seqKey,
    func(ctx context.Context, ref *kitsune.Ref[int], e Event) (NumberedEvent, error) {
        n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
        if err != nil {
            return NumberedEvent{}, err
        }
        return NumberedEvent{Seq: n, Event: e}, nil
    },
)
```

---

### FlatMapWith

```go
func FlatMapWith[I, O, S any](
    p *Pipeline[I],
    key Key[S],
    fn func(context.Context, *Ref[S], I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Like [`FlatMap`](#flatmap) but with a `*Ref[S]` for persistent state. Each input can produce zero or more outputs while reading and writing state.

**Options:** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`.

```go
var windowKey = kitsune.NewKey[[]Event]("window", nil)

// Emit a window every 5 events.
windows := kitsune.FlatMapWith(events, windowKey,
    func(ctx context.Context, ref *kitsune.Ref[[]Event], e Event, yield func([]Event) error) error {
        buf, _ := ref.Get(ctx)
        buf = append(buf, e)
        if len(buf) >= 5 {
            if err := yield(append([]Event(nil), buf...)); err != nil {
                return err
            }
            buf = buf[:0]
        }
        return ref.Set(ctx, buf)
    },
)
```

---

### MapWithKey

```go
func MapWithKey[I, O, S any](
    p *Pipeline[I],
    keyFn func(I) string,
    key Key[S],
    fn func(context.Context, *Ref[S], I) (O, error),
    opts ...StageOption,
) *Pipeline[O]
```

Like [`MapWith`](#mapwith) but maintains one independent `Ref[S]` per entity key (derived from each item via `keyFn`). Items with the same key share state; items with different keys are isolated. This enables per-user, per-session, or per-device aggregation in a single stage.

**When to use:** Per-entity state in a multiplexed stream: event counts per user, session tracking, per-device rate limiting.

**Options:** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`, `WithKeyTTL`.

```go
var countKey = kitsune.NewKey[int]("event_count", 0)

counted := kitsune.MapWithKey(events,
    func(e Event) string { return e.UserID },
    countKey,
    func(ctx context.Context, ref *kitsune.Ref[int], e Event) (Result, error) {
        n, err := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
        if err != nil {
            return Result{}, err
        }
        return Result{UserID: e.UserID, Count: n}, nil
    },
)
```

**High-cardinality eviction:** On long-running pipelines with unbounded key spaces (user IDs, session tokens), use `WithKeyTTL(d)` to evict entries that have been inactive for longer than `d`. The next item for an evicted key starts from the initial value. Eviction is lazy (checked on the next access; no background goroutine). `WithKeyTTL` is independent of `StateTTL`: `StateTTL` expires the value held by a `Ref`; `WithKeyTTL` expires the map entry that holds the `Ref`.

```go
// Evict per-user state after 15 minutes of inactivity.
kitsune.MapWithKey(events,
    func(e Event) string { return e.UserID },
    sessionKey,
    handler,
    kitsune.WithKeyTTL(15*time.Minute),
)
```

The run-level `WithDefaultKeyTTL(d)` sets the default TTL for all `MapWithKey` and `FlatMapWithKey` stages that do not specify their own `WithKeyTTL`. Per-stage `WithKeyTTL(0)` explicitly disables eviction even when a run-level default is set.

**Supervise and state lifetime:** When combined with `Supervise`, per-key `Ref` state IS preserved across supervised restarts within a single `Run` call: the keyed map is allocated once per run and captured by the stage's restarted loop, so a panic or error that triggers a restart does not zero the accumulated state. State is NOT preserved across separate `Run` calls with the default in-process store; callers that need cross-run durability must configure an external Store via `WithStore`.

---

### FlatMapWithKey

```go
func FlatMapWithKey[I, O, S any](
    p *Pipeline[I],
    keyFn func(I) string,
    key Key[S],
    fn func(context.Context, *Ref[S], I, func(O) error) error,
    opts ...StageOption,
) *Pipeline[O]
```

Like `MapWithKey` but allows emitting zero or more outputs per item while maintaining per-key state.

**Options:** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`, `Supervise`, `WithKeyTTL`.

---

## :material-layers-outline: Batching & Windowing { #batching }

### BufferWith

```go
func BufferWith[T, S any](p *Pipeline[T], closingSelector *Pipeline[S], opts ...StageOption) *Pipeline[[]T]
```

Collects items from `p` into a slice, emitting the accumulated buffer each time `closingSelector` fires. An empty buffer is never emitted. When the source closes, any remaining buffered items are flushed before the output closes. When `closingSelector` closes, any remaining buffered items are flushed and the output closes.

`BufferWith` generalizes [`Batch`](#batch) (fixed-size boundary) and `BatchTimeout` (periodic boundary) to arbitrary external boundary signals. Use it when the flush trigger is externally driven: heartbeats, quiescence signals, control channels, or upstream events.

**When to use:**
- Flushing accumulated events on an external heartbeat or tick pipeline.
- Coalescing bursts until a quiescence signal arrives on a separate channel.
- Building custom batching policies that go beyond size or time.

**Semantics:**
- Items are emitted in input order; the flattened output preserves source ordering.
- If `closingSelector` fires while the buffer is empty, no batch is emitted.
- If `closingSelector` closes before the source, any remaining source items are not read.
- Context cancellation returns `ctx.Err()` without flushing.
- Panics if `closingSelector` is nil.

**Options:** `Buffer`, `WithName`.

```go
// Flush buffered events on every heartbeat tick.
heartbeat := kitsune.Ticker(5 * time.Second)
batches := kitsune.BufferWith(events, heartbeat)
```

---

### Batch

```go
func Batch[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T]
```

Collects items into `[]T` slices of up to `size` elements. When the source closes, any remaining items are flushed as a partial batch. An empty batch is never emitted.

With `BatchTimeout(d)`, a partial batch is also flushed when the timeout elapses; this is useful for low-throughput streams where you do not want to wait for a full batch.

**When to use:** Bulk database inserts, batched API calls, reducing per-item overhead for expensive operations.

**When not to use:** When you need overlapping windows; use [`SlidingWindow`](#slidingwindow) or `SessionWindow`.

With `DropPartial()`, the final partial batch is discarded when the source closes; only full batches are emitted.

**Options:** `BatchTimeout`, `DropPartial`, `WithClock`, `Buffer`, `WithName`.

```go
// Flush up to 100 items at a time, or after 500ms.
batches := kitsune.Batch(events, 100, kitsune.BatchTimeout(500*time.Millisecond))

// Emit only full batches of 10; drop any trailing items.
chunks := kitsune.Batch(items, 10, kitsune.DropPartial())
```

---

### MapBatch

```go
func MapBatch[I, O any](
    p *Pipeline[I],
    size int,
    fn func(context.Context, []I) ([]O, error),
    opts ...StageOption,
) *Pipeline[O]
```

Collects items into batches of up to `size`, passes each batch to `fn`, and flattens the results back to individual items. This is syntactic sugar for [`Batch`](#batch) + [`FlatMap`](#flatmap) and is the right primitive for bulk external calls where output count equals input count (e.g., bulk database lookups).

The `fn` must return the same number of results as items in the batch.

**Options:** `BatchTimeout`, `Concurrency`, `OnError`, `Buffer`, `WithName`.

```go
enriched := kitsune.MapBatch(userIDs, 200,
    func(ctx context.Context, ids []int) ([]User, error) {
        return db.BulkFetchUsers(ctx, ids)
    },
)
```

---

### SlidingWindow

```go
func SlidingWindow[T any](p *Pipeline[T], size, step int, opts ...StageOption) *Pipeline[[]T]
```

Emits overlapping slices of exactly `size` items, advancing by `step` items each time. When `step == size`, this is a non-overlapping tumbling window. When `step < size`, windows overlap. Partial windows at the end of the stream are dropped. Panics if `step <= 0` or `step > size`.

**When to use:** Rolling averages, sliding statistics, n-gram generation.

**Options:** `Buffer`, `WithName`.

```go
// Moving average over 5-item windows, advancing 1 at a time.
windows := kitsune.SlidingWindow(prices, 5, 1)
```

---

### SessionWindow

```go
func SessionWindow[T any](p *Pipeline[T], gap time.Duration, opts ...StageOption) *Pipeline[[]T]
```

Groups items into sessions separated by periods of inactivity. A new session starts whenever no item arrives within `gap`. The accumulated session buffer is emitted when the gap timer fires. An empty session is never emitted. The final partial session is emitted when the source closes.

**When to use:** User session detection, grouping related events that occur close together in time.

**Options:** `WithClock`, `Buffer`, `WithName`.

```go
// Group clicks into sessions with a 30-second inactivity timeout.
sessions := kitsune.SessionWindow(clickEvents, 30*time.Second)
```

---

### ChunkBy

```go
func ChunkBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[[]T]
```

Groups consecutive items that share the same key (returned by `keyFn`) into slices. A new chunk begins whenever the key changes. The last chunk is emitted when the source completes.

**When to use:** Run-length grouping: group consecutive log lines by severity, transactions by account.

**Options:** `Buffer`, `WithName`.

```go
// [1,1,2,2,1] → [[1,1],[2,2],[1]]
kitsune.ChunkBy(nums, func(n int) int { return n })
```

---

### ChunkWhile

```go
func ChunkWhile[T any](p *Pipeline[T], pred func(prev, curr T) bool, opts ...StageOption) *Pipeline[[]T]
```

Groups consecutive items into chunks while `pred(prev, current)` returns `true`. A new chunk begins when `pred` returns `false`.

**Options:** `Buffer`, `WithName`.

```go
// Group ascending runs: [1,2,3,1,2] → [[1,2,3],[1,2]]
kitsune.ChunkWhile(nums, func(prev, curr int) bool { return curr > prev })
```

---

### GroupByStream

```go
func GroupByStream[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[Group[K, T]]
```

Partitions items by key and emits one `Group[K, T]` per distinct key when the source completes, in first-seen key order. For a terminal map result use [`GroupBy`](#tomap-groupby-frequencies-frequenciesby).

```go
type Group[K comparable, V any] struct {
    Key   K
    Items []V
}
```

**Options:** `Buffer`, `WithName`.

```go
grouped := kitsune.GroupByStream(events, func(e Event) string { return e.Type })
// emits one Group per distinct e.Type, with all matching events
```

---

## :material-source-branch: Fan-Out & Fan-In { #fan-out-fan-in }

### Merge

```go
func Merge[T any](pipelines ...*Pipeline[T]) *Pipeline[T]
```

Combines multiple pipelines of the same type into one. Items are emitted as they arrive from any source (race order). The merged pipeline completes when all inputs have completed.

**When to use:** Combining results from multiple concurrent sources: multiple Kafka partitions, multiple API endpoints running in parallel.

**Options:** none (buffer is fixed internally).

```go
merged := kitsune.Merge(partitionA, partitionB, partitionC)
```

---

### Partition

```go
func Partition[T any](p *Pipeline[T], pred func(T) bool, opts ...StageOption) (*Pipeline[T], *Pipeline[T])
```

Splits a pipeline into two: items for which `pred` returns `true` go to the first pipeline; items for which `pred` returns `false` go to the second. Both pipelines must be consumed; use [`MergeRunners`](#mergerunners) to run them together.

**Options:** `Buffer`, `WithName`.

```go
valid, invalid := kitsune.Partition(records, func(r Record) bool { return r.Valid })

r1 := valid.ForEach(store).Build()
r2 := invalid.ForEach(logInvalid).Build()
runner, _ := kitsune.MergeRunners(r1, r2)
runner.Run(ctx)
```

---

### Broadcast

```go
func Broadcast[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T]
```

Fans out each item to `n` identical output pipelines. Every item is delivered to every branch (synchronised fan-out). A slow branch backpressures upstream and all other branches. All `n` pipelines must be consumed. Panics if `n < 2`.

**When to use:** When you know the exact number of consumers at construction time and need each one to see every item: metrics + storage + audit.

**Options:** `Buffer`, `WithName`.

```go
branches := kitsune.Broadcast(events, 3)
r1 := branches[0].ForEach(storeEvent).Build()
r2 := branches[1].ForEach(updateMetrics).Build()
r3 := branches[2].ForEach(auditLog).Build()
runner, _ := kitsune.MergeRunners(r1, r2, r3)
runner.Run(ctx)
```

---

### Share

```go
func Share[T any](p *Pipeline[T], opts ...StageOption) func(...StageOption) *Pipeline[T]
```

Returns a subscription factory for dynamic multicast. Call the returned function once per desired branch; each branch receives every item from `p`. Unlike [`Broadcast`](#broadcast), the number of consumers does not need to be known upfront; branches are registered dynamically before `Run` is called.

Options passed to `Share` are defaults for all branches; options passed to individual subscribe calls override them per-branch.

**When to use:** When consumers are built in a loop, from config, or from a plugin registry, when [`Broadcast`](#broadcast)'s fixed `n` is inconvenient.

**Options on the factory:** `Buffer`, `WithName` (defaults for all branches). Per-subscribe calls can also pass `Buffer`, `WithName`.

```go
subscribe := kitsune.Share(events)

audit   := subscribe(kitsune.WithName("audit"),   kitsune.Buffer(1000))
metrics := subscribe(kitsune.WithName("metrics"), kitsune.Buffer(16))
if cfg.FraudEnabled {
    fraud = subscribe(kitsune.WithName("fraud"))
}

r1 := audit.ForEach(writeAudit).Build()
r2 := metrics.ForEach(updateMetrics).Build()
runner, _ := kitsune.MergeRunners(r1, r2)
runner.Run(ctx)
```

---

### Balance

```go
func Balance[T any](p *Pipeline[T], n int, opts ...StageOption) []*Pipeline[T]
```

Distributes items across `n` output pipelines in round-robin order. Each item goes to exactly one output. All `n` pipelines must be consumed.

**When to use:** Spreading load across `n` identical workers, each maintaining its own connection pool or state.

**Options:** `Buffer`, `WithName`.

```go
branches := kitsune.Balance(jobs, 4)
runners := make([]*kitsune.Runner, 4)
for i, b := range branches {
    runners[i] = b.ForEach(worker).Build()
}
runner, _ := kitsune.MergeRunners(runners...)
runner.Run(ctx)
```

---

### KeyedBalance

```go
func KeyedBalance[T any](p *Pipeline[T], n int, keyFn func(T) string, opts ...StageOption) []*Pipeline[T]
```

Distributes items across `n` output pipelines by consistent hash of `keyFn(item)`. All items with the same key always go to the same branch, enabling per-entity parallelism without cross-branch coordination. Pairs well with `MapWithKey` for sharded stateful workloads.

**Options:** `Buffer`, `WithName`.

```go
// All events for the same userID go to the same branch.
branches := kitsune.KeyedBalance(events, 8, func(e Event) string { return e.UserID })
```

---

### Zip / ZipWith

```go
func Zip[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]]

func ZipWith[A, B, O any](
    a *Pipeline[A],
    b *Pipeline[B],
    fn func(context.Context, A, B) (O, error),
    opts ...StageOption,
) *Pipeline[O]
```

`Zip` pairs items from `a` and `b` positionally into `Pair[A, B]` values. [`ZipWith`](#zip-zipwith) pairs them and transforms the pair using `fn`. The pipeline completes when either input completes; the other's remaining items are discarded.

**When to use:** Correlating two aligned streams positionally: test inputs with expected outputs, requests with responses.

**Options (ZipWith):** `Buffer`, `WithName`.

```go
// Correlate requests with responses.
pairs := kitsune.Zip(requests, responses)
```

---

### Unzip

```go
func Unzip[A, B any](p *Pipeline[Pair[A, B]], opts ...StageOption) (*Pipeline[A], *Pipeline[B])
```

Splits a pipeline of `Pair[A, B]` into two separate pipelines. Both output pipelines must be consumed.

**Options:** `Buffer`, `WithName`.

```go
aP, bP := kitsune.Unzip(pairs)
```

---

### LatestFrom / LatestFromWith

```go
func LatestFrom[A, B any](main *Pipeline[A], other *Pipeline[B]) *Pipeline[Pair[A, B]]

func LatestFromWith[A, B, O any](
    main *Pipeline[A],
    other *Pipeline[B],
    fn func(context.Context, A, B) (O, error),
    opts ...StageOption,
) *Pipeline[O]
```

Combines each item from `main` with the most-recently-seen item from `other`. Items from `main` are only emitted after `other` has emitted at least one item. Items from `other` that arrive between `main` items update the "latest" snapshot but are not independently emitted.

This models a "sample the latest state of other on each main event" pattern: `main` drives the output rate; `other` provides current state.

**When to use:** Combining a high-frequency event stream with a low-frequency configuration or rate stream, e.g., apply the latest exchange rate to each transaction.

**Options (LatestFromWith):** `Buffer`, `WithName`.

```go
// Apply the latest config to each incoming request.
processed := kitsune.LatestFrom(requests, configUpdates)
// Each Pair has: First=request, Second=most-recent config
```

---

### CombineLatest / CombineLatestWith

```go
func CombineLatest[A, B any](a *Pipeline[A], b *Pipeline[B]) *Pipeline[Pair[A, B]]

func CombineLatestWith[A, B, O any](
    a *Pipeline[A],
    b *Pipeline[B],
    fn func(context.Context, A, B) (O, error),
    opts ...StageOption,
) *Pipeline[O]
```

Emits a new value whenever either `a` or `b` emits, combining the latest values from each. Emitting begins only after both pipelines have emitted at least one item. Unlike [`LatestFrom`](#latestfrom--latestfromwith), both pipelines drive the output.

**When to use:** UI state combinations, sensor fusion where you want a new output whenever either reading changes, e.g., combine temperature and humidity sensors into a comfort index.

**Options (CombineLatestWith):** `Buffer`, `WithName`.

```go
// Recompute risk score whenever either signal updates.
risk := kitsune.CombineLatestWith(creditScore, marketIndex,
    func(ctx context.Context, cs CreditScore, mi MarketIndex) (RiskScore, error) {
        return computeRisk(cs, mi), nil
    },
)
```

---

## :material-database-plus-outline: Enrichment { #enrichment }

Enrichment operators bulk-fetch external data for a batch of items and attach it to each item. Keys are deduplicated before each fetch call; if multiple items share a key, only one lookup is made.

### LookupBy

```go
func LookupBy[T any, K comparable, V any](
    p *Pipeline[T],
    cfg LookupConfig[T, K, V],
    opts ...StageOption,
) *Pipeline[Enriched[T, V]]
```

Enriches each item with a value fetched in bulk, emitting `Enriched[T, V]` (fields `Item` and `Value`). Items whose key is absent from the fetch result carry the zero value for `V`. `LookupConfig` carries:

- `Key func(T) K`: extracts the lookup key from each item
- `Fetch func(context.Context, []K) (map[K]V, error)`: bulk fetcher
- `BatchSize int`: how many items to collect before calling `Fetch` (default: 100)
- `BatchTimeout time.Duration`: when non-zero, flushes a partial batch after the duration elapses with no new item. Without this, items sit in the internal buffer until `BatchSize` is reached or the source closes, which can introduce unbounded latency under low throughput.

**Options:** `Buffer`, `WithName`, `BatchTimeout`.

```go
cfg := kitsune.NewLookupConfig(
    func(e Event) string { return e.UserID },
    func(ctx context.Context, ids []string) (map[string]User, error) {
        return userDB.BulkFetch(ctx, ids)
    },
)
withUsers := kitsune.LookupBy(events, cfg)
// each item: Enriched[Event, User]{Item: event, Value: user}
```

---

### Enrich

```go
func Enrich[T any, K comparable, V, O any](
    p *Pipeline[T],
    cfg EnrichConfig[T, K, V, O],
    opts ...StageOption,
) *Pipeline[O]
```

Like [`LookupBy`](#lookupby) but calls a `Join` function to combine the item and its fetched value into the output type directly, without an intermediate `Pair`. `EnrichConfig` carries:

- `Key func(T) K`
- `Fetch func(context.Context, []K) (map[K]V, error)`
- `Join func(T, V) O`
- `BatchSize int`: default 100
- `BatchTimeout time.Duration`: when non-zero, flushes a partial batch after the duration elapses with no new item.

**Options:** `Buffer`, `WithName`, `BatchTimeout`.

```go
cfg := kitsune.NewEnrichConfig(
    func(e Event) string { return e.UserID },
    func(ctx context.Context, ids []string) (map[string]User, error) {
        return userDB.BulkFetch(ctx, ids)
    },
    func(e Event, u User) EnrichedEvent {
        return EnrichedEvent{Event: e, UserName: u.Name}
    },
)
enriched := kitsune.Enrich(events, cfg)
```

---

## :material-sigma: Aggregation & Collection { #aggregation }

### Scan

```go
func Scan[T, S any](p *Pipeline[T], initial S, fn func(S, T) S, opts ...StageOption) *Pipeline[S]
```

Accumulates running state across items using `fn`, emitting the running state after each item. The first emission is `fn(initial, firstItem)`. Unlike [`Reduce`](#reduce), `Scan` emits intermediate states as items arrive rather than waiting for the source to complete.

**Options:** `Buffer`, `WithName`.

```go
// Running total of prices.
runningTotal := kitsune.Scan(prices, 0.0, func(acc float64, p float64) float64 {
    return acc + p
})
```

---

### Reduce

```go
func Reduce[T, S any](p *Pipeline[T], initial S, fn func(S, T) S, opts ...StageOption) *Pipeline[S]
```

Folds all items into a single value using `fn`. The result is emitted exactly once when the source completes. If the source emits no items, `initial` is emitted. Unlike [`Scan`](#scan), no intermediate values are emitted.

**Options:** `Buffer`, `WithName`.

```go
total := kitsune.Reduce(prices, 0.0, func(acc, p float64) float64 { return acc + p })
// emits one value: the sum of all prices
```

---

### Sum / Min / Max / MinMax

```go
func Sum[T Numeric](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, error)
func Min[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error)
func Max[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (T, bool, error)
func MinMax[T any](ctx context.Context, p *Pipeline[T], less func(a, b T) bool, opts ...RunOption) (MinMaxResult[T], bool, error)
```

Terminal aggregators. `Sum` works on any `Numeric` type. `Min` and `Max` take a `less` comparator and return `(zero, false, nil)` if the pipeline is empty. `MinMax` computes both in a single pass and returns a `MinMaxResult[T]` with fields `Min` and `Max`.

```go
total, err := kitsune.Sum(ctx, prices)
min, ok, err := kitsune.Min(ctx, prices, func(a, b float64) bool { return a < b })
```

---

### MinBy / MaxBy

```go
func MinBy[T any, K any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error)
func MaxBy[T any, K any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...RunOption) (T, bool, error)
```

Like `Min`/`Max` but compares items by a key derived from `keyFn`.

```go
cheapest, ok, err := kitsune.MinBy(ctx, products,
    func(p Product) float64 { return p.Price },
    func(a, b float64) bool { return a < b },
)
```

---

### ReduceWhile

```go
func ReduceWhile[T, S any](ctx context.Context, p *Pipeline[T], initial S, fn func(S, T) (S, bool), opts ...RunOption) (S, error)
```

Folds items until `fn` signals stop by returning `(state, false)`. The current state is returned immediately without consuming further items.

Also available as `p.ReduceWhile(ctx, initial, fn)`.

```go
// Sum until we exceed 1000.
partial, _ := kitsune.ReduceWhile(ctx, prices, 0.0,
    func(acc float64, p float64) (float64, bool) {
        next := acc + p
        return next, next <= 1000.0
    },
)
```

---

### TakeRandom

```go
func TakeRandom[T any](ctx context.Context, p *Pipeline[T], n int, opts ...RunOption) ([]T, error)
```

Returns a random sample of up to `n` items using reservoir sampling (Algorithm R). Each item has an equal probability of being selected. The returned slice has `min(n, pipelineSize)` items. Order of the returned items is not guaranteed.

```go
sample, err := kitsune.TakeRandom(ctx, users, 100)
```

---

### Collect / First / Last / Count / Any / All / Find / Contains

```go
func Collect[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) ([]T, error)
func First[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, bool, error)
func Last[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (T, bool, error)
func Count[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (int64, error)
func Any[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error)
func All[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (bool, error)
func Find[T any](ctx context.Context, p *Pipeline[T], pred func(T) bool, opts ...RunOption) (T, bool, error)
func Contains[T comparable](ctx context.Context, p *Pipeline[T], value T, opts ...RunOption) (bool, error)
```

Terminal collectors. All run the pipeline and block until completion. `First`, `Any`, `All`, `Find`, and `Contains` short-circuit; they stop the pipeline as soon as the answer is known. `First` and `Last` return `(zero, false, nil)` if the pipeline is empty.

All are also available as methods on `*Pipeline[T]` (except `Find` and `Contains`, which require type parameters).

```go
items, err  := kitsune.Collect(ctx, p)
first, ok, err := kitsune.First(ctx, p)
n, err      := kitsune.Count(ctx, p)
found, err  := kitsune.Any(ctx, p, func(v int) bool { return v > 0 })
```

---

### ToMap / GroupBy / Frequencies / FrequenciesBy

```go
func ToMap[T any, K comparable, V any](ctx context.Context, p *Pipeline[T], keyFn func(T) K, valueFn func(T) V, opts ...RunOption) (map[K]V, error)
func GroupBy[T any, K comparable](ctx context.Context, p *Pipeline[T], keyFn func(T) K, opts ...RunOption) (map[K][]T, error)
func Frequencies[T comparable](ctx context.Context, p *Pipeline[T], opts ...RunOption) (map[T]int, error)
func FrequenciesBy[T any, K comparable](ctx context.Context, p *Pipeline[T], keyFn func(T) K, opts ...RunOption) (map[K]int, error)
```

Terminal aggregators that return maps. `ToMap` uses last-writer-wins for duplicate keys. [`Frequencies`](#tomap-groupby-frequencies-frequenciesby) and `FrequenciesBy` count occurrences.

```go
byID, err   := kitsune.ToMap(ctx, users, func(u User) int { return u.ID }, func(u User) User { return u })
groups, err := kitsune.GroupBy(ctx, events, func(e Event) string { return e.Type })
counts, err := kitsune.Frequencies(ctx, kitsune.Map(events, extractType))
```

---

### FrequenciesStream / FrequenciesByStream

```go
func FrequenciesStream[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[map[T]int64]
func FrequenciesByStream[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[map[K]int64]
```

Like [`Frequencies`](#tomap-groupby-frequencies-frequenciesby)/`FrequenciesBy` but emits a single `map` snapshot when the source completes, as a pipeline item. Use this when you want to pipeline the counts into further stages.

**Options:** `Buffer`, `WithName`.

---

### SequenceEqual

```go
func SequenceEqual[T comparable](ctx context.Context, a, b *Pipeline[T], opts ...RunOption) (bool, error)
```

Returns `true` if `a` and `b` emit the same items in the same order and have the same length.

---

### Iter

```go
func Iter[T any](ctx context.Context, p *Pipeline[T], opts ...RunOption) (iter.Seq[T], func() error)
```

Returns a Go 1.23 `iter.Seq[T]` iterator and an error function. Use the iterator with `range`. Call the error function after the loop (or after breaking out) to retrieve any pipeline error. Breaking out of the loop cancels the pipeline; the error function returns `nil` in that case.

Also available as `p.Iter(ctx)`.

```go
seq, errFn := kitsune.Iter(ctx, p)
for item := range seq {
    process(item)
}
if err := errFn(); err != nil {
    log.Fatal(err)
}
```

---

### Sort / SortBy

```go
func Sort[T any](p *Pipeline[T], less func(a, b T) bool, opts ...StageOption) *Pipeline[T]
func SortBy[T any, K any](p *Pipeline[T], keyFn func(T) K, less func(a, b K) bool, opts ...StageOption) *Pipeline[T]
```

Collects all items, sorts them, then emits in sorted order. The source pipeline must be finite. This is a blocking, memory-intensive operation; the entire stream is buffered before any output is emitted.

**Options:** `Buffer`, `WithName`.

```go
sorted := kitsune.Sort(items, func(a, b Item) bool { return a.Timestamp.Before(b.Timestamp) })
```

---

## :material-clock-outline: Time-Based Operators { #time-based }

### Throttle

```go
func Throttle[T any](p *Pipeline[T], window time.Duration, opts ...StageOption) *Pipeline[T]
```

Emits at most one item per `window` duration. The first item in each window is emitted; subsequent items within the same window are silently dropped. This is "throttle-leading" or rate-limiting on item arrival.

**When to use:** Limiting how often a downstream stage is called, suppressing rapid event bursts while keeping the first event in each burst.

**Options:** `WithClock`, `Buffer`, `WithName`.

```go
// At most one notification per 10 seconds.
throttled := kitsune.Throttle(alerts, 10*time.Second)
```

---

### Debounce

```go
func Debounce[T any](p *Pipeline[T], silence time.Duration, opts ...StageOption) *Pipeline[T]
```

Suppresses rapid bursts: an item is only emitted after no new items have arrived for `silence`. If items arrive faster than `silence`, only the last item in each burst is forwarded. The last pending item is flushed when the source closes.

**When to use:** Typeahead debouncing, saving documents after a user stops typing, coalescing rapid config changes.

**Options:** `WithClock`, `Buffer`, `WithName`.

```go
// Fire a search only after 300ms of silence.
queries := kitsune.Debounce(keystrokes, 300*time.Millisecond)
```

---

### Sample

```go
func Sample[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[T]
```

Emits the most-recently-seen item from `p` at each tick of a `d`-duration interval. If no item has arrived since the last tick, that tick is skipped. Unlike [`Throttle`](#throttle) (which fires on item arrival) and [`Debounce`](#debounce) (which waits for quiet), `Sample` fires at a fixed wall-clock rate.

**When to use:** Live dashboards, periodic snapshots of high-frequency streams.

**Options:** `WithClock`, `Buffer`, `WithName`.

```go
// Emit the latest quote at most once every 100ms.
sampled := kitsune.Sample(liveQuotes, 100*time.Millisecond)
```

---

### SampleWith

```go
func SampleWith[T, S any](p *Pipeline[T], sampler *Pipeline[S], opts ...StageOption) *Pipeline[T]
```

Emits the most recent item from `p` whenever the `sampler` pipeline fires. If no item has arrived since the last sampler signal, that signal is skipped silently. The latest item is consumed on emit: if the sampler fires twice without a new source item arriving in between, only the first fire emits.

Unlike [`Sample`](#sample) (driven by a fixed wall-clock interval) and [`Throttle`](#throttle) (rate-limits on item arrival), `SampleWith` is driven by an arbitrary pipeline. The sampler's item values are discarded; only the occurrence of each item matters.

The pipeline completes when the sampler closes. If the source closes and its last item has already been emitted, the pipeline also completes early.

**When to use:** Polling a high-frequency stream at an externally defined rate — for example, sampling sensor readings on each heartbeat, or snapshotting the latest price whenever a timer ticks.

**Options:** `Buffer`, `WithName`.

```go
// Emit the latest quote once per second, driven by a Ticker.
clock  := kitsune.Ticker(1 * time.Second)
polled := kitsune.SampleWith(liveQuotes, clock)

// Snapshot the latest sensor value on each heartbeat signal.
snapped := kitsune.SampleWith(sensorStream, heartbeatPipeline)
```

---

### RateLimit

```go
func RateLimit[T any](
    p *Pipeline[T],
    ratePerSec float64,
    rlOpts []RateLimitOpt,
    stageOpts ...StageOption,
) *Pipeline[T]
```

Limits throughput to `ratePerSec` items per second using a token bucket. In `RateLimitWait` mode (default), the pipeline blocks when the bucket is empty (backpressure). In `RateLimitDrop` mode, excess items are silently discarded.

Rate-limit options (`rlOpts`):
- `Burst(n)`: allow short bursts of up to `n` tokens above the steady rate
- `RateMode(RateLimitDrop)`: drop items instead of blocking

**Options (stageOpts):** `Buffer`, `WithName`.

```go
// Allow up to 100 events/sec with bursts of up to 10.
limited := kitsune.RateLimit(events, 100, []kitsune.RateLimitOpt{kitsune.Burst(10)})

// Drop items that exceed 50/sec.
lossy := kitsune.RateLimit(events, 50, []kitsune.RateLimitOpt{kitsune.RateMode(kitsune.RateLimitDrop)})
```

---

## :material-shield-check-outline: Resilience { #resilience }

### CircuitBreaker

```go
func CircuitBreaker[I, O any](
    p *Pipeline[I],
    fn func(context.Context, I) (O, error),
    cbOpts []CircuitBreakerOpt,
    stageOpts ...StageOption,
) *Pipeline[O]
```

Wraps `fn` in a three-state circuit breaker:

- **Closed** (normal): `fn` is called for every item. Consecutive failures increment a counter.
- **Open** (tripped): after `FailureThreshold` consecutive failures, the circuit opens. All items immediately receive `ErrCircuitOpen` without calling `fn`. The circuit stays open for `CooldownDuration`.
- **Half-open** (probing): after the cooldown, up to `HalfOpenProbes` items are tested. If all succeed, the circuit closes. If any fail, the circuit opens again.

The circuit breaker is built on top of [`Map`](#map), so all `StageOption` values apply. Use `OnError(Skip())` to silently drop items while the circuit is open, or `OnError(Return(zero))` to substitute a default.

Circuit-breaker options (`cbOpts`):
- `FailureThreshold(n)`: consecutive failures to open (default: 5)
- `CooldownDuration(d)`: open duration before probing (default: 10s)
- `HalfOpenProbes(n)`: successes required to close from half-open (default: 1)
- `HalfOpenTimeout(d)`: deadline on the half-open state

**Options (stageOpts):** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`.

```go
results := kitsune.CircuitBreaker(requests, callExternalAPI,
    []kitsune.CircuitBreakerOpt{
        kitsune.FailureThreshold(3),
        kitsune.CooldownDuration(30 * time.Second),
        kitsune.HalfOpenProbes(2),
    },
    kitsune.OnError(kitsune.Skip()), // drop items while open
    kitsune.Concurrency(4),
)
```

---

### MapPooled

```go
func MapPooled[I, O any](
    p *Pipeline[I],
    pool *Pool[O],
    fn func(context.Context, I, *Pooled[O]) error,
    opts ...StageOption,
) *Pipeline[*Pooled[O]]
```

Transforms each item using `fn`, acquiring a pre-allocated `*Pooled[O]` from `pool` before each call. The result is the filled `*Pooled[O]` wrapper. Downstream code must call `Release()` on each received item (or `ReleaseAll` for batches); failing to release leaks pool objects.

If `fn` returns an error, the slot is automatically released back to the pool.

**Use-after-release protection:** `Release()` panics on double-call, so misuse is caught early rather than causing silent data corruption. Use `buf.MustValue()` instead of `buf.Value` when you want a panic on access after release (zero-overhead `Value` remains available for hot paths where you control the lifecycle).

**When to use:** High-throughput transforms where allocating a new output buffer per item is expensive: JSON encoding, protobuf marshalling, audio/video frame processing.

**Options:** `Concurrency`, `OnError`, `Buffer`, `Overflow`, `WithName`, `Timeout`.

```go
pool := kitsune.NewPool(func() []byte { return make([]byte, 0, 4096) })
encoded := kitsune.MapPooled(events, pool,
    func(ctx context.Context, e Event, out *kitsune.Pooled[[]byte]) error {
        var err error
        out.Value, err = json.Marshal(e)
        return err
    })

// Downstream: release each buffer after use.
encoded.ForEach(func(_ context.Context, buf *kitsune.Pooled[[]byte]) error {
    defer buf.Release()
    return conn.Write(buf.Value)
}).Run(ctx)
```

---

## :material-tools: Utility & Metadata { #utility }

### WithIndex

```go
func WithIndex[T any](p *Pipeline[T], opts ...StageOption) *Pipeline[Indexed[T]]
```

Tags each item with its 0-based stream position. Emits `Indexed[T]{Index int; Value T}`.

**Options:** `Buffer`, `WithName`.

```go
kitsune.WithIndex(items)
// emits: {0, first}, {1, second}, {2, third}, …
```

---

### Tap / TapError / Finally

```go
func Tap[T any](p *Pipeline[T], fn func(context.Context, T) error, opts ...StageOption) *Pipeline[T]
func TapError[T any](p *Pipeline[T], fn func(context.Context, error)) *Pipeline[T]
func Finally[T any](p *Pipeline[T], fn func(context.Context, error)) *Pipeline[T]
```

Side-effect operators that forward all items unchanged.

- `Tap` calls `fn` for each item as a side effect. If `fn` returns an error, the pipeline halts.
- `TapError` calls `fn` when the pipeline terminates with an error, then re-propagates the error.
- `Finally` calls `fn` when the pipeline exits for any reason (success, error, or cancellation), then re-propagates the outcome.

The method forms accept simpler signatures: `p.Tap(func(T))`, `p.TapError(func(error))`, `p.Finally(func(error))`.

**Options (Tap):** `Buffer`, `WithName`.

```go
p.Tap(func(e Event) { metrics.Inc("events_processed") }).
  TapError(func(err error) { log.Printf("pipeline error: %v", err) }).
  Finally(func(err error) { conn.Close() }).
  ForEach(store).Run(ctx)
```

---

### Scan (see Aggregation section)

---

## :material-flag-checkered: Terminal Operators { #terminals }

### ForEach

```go
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *ForEachRunner[T]
```

Returns a `ForEachRunner` that calls `fn` for every item. No processing occurs until `Run` or `RunAsync` is called.

`ForEachRunner` has:
- `Run(ctx, opts...)`: blocks until complete
- `Build()`: returns a `Runner` for use with [`MergeRunners`](#mergerunners)

With `Concurrency(n)` > 1, `fn` is called from `n` goroutines. Add `Ordered()` to call `fn` in input order even with concurrency.

**Options:** `Concurrency`, `Ordered`, `OnError`, `Buffer`, `WithName`, `Timeout`, `Supervise`.

```go
err := events.ForEach(func(ctx context.Context, e Event) error {
    return db.Insert(ctx, e)
}, kitsune.Concurrency(8), kitsune.OnError(kitsune.RetryMax(3, kitsune.FixedBackoff(100*time.Millisecond)))).
    Run(ctx)
```

---

### Drain

```go
func (p *Pipeline[T]) Drain() *DrainRunner[T]
```

Discards all items. Useful for running a pipeline for its side effects (e.g., when all work is done by `Tap` stages) without collecting any output.

```go
p.Tap(func(e Event) { process(e) }).Drain().Run(ctx)
```

---

### Runner / RunAsync

```go
func (r *Runner) Run(ctx context.Context, opts ...RunOption) error
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) *RunHandle
```

`Run` executes the pipeline, blocking until completion. `RunAsync` starts the pipeline in a background goroutine and returns a `RunHandle`.

`RunHandle` provides:
- `Wait() error`: block until done
- `Done() <-chan struct{}`: closed when done
- `Err() <-chan error`: receives exactly one value
- `Pause()` / `Resume()` / `Paused()`: pause/resume source stages

**Run options:**
- `WithStore(s Store)`: state backend for [`MapWith`](#mapwith), [`FlatMapWith`](#flatmapwith)
- `WithHook(h Hook)`: observability hook
- `WithDrain(timeout)`: graceful drain on context cancellation
- `WithCache(cache, ttl)`: default cache backend for `CacheBy` stages
- `WithErrorStrategy(h)`: pipeline-wide default error handler
- `WithPauseGate(gate)`: attach an external gate
- `WithCodec(c)`: serialisation codec for state and cache
- `WithDefaultBuffer(n)`: default channel buffer size for all stages (default 16); per-stage `Buffer(n)` takes precedence
- `WithDefaultKeyTTL(d)`: default inactivity TTL for all [`MapWithKey`](#mapwithkey) and [`FlatMapWithKey`](#flatmapwithkey) stages; per-stage `WithKeyTTL` takes precedence

```go
handle := runner.RunAsync(ctx)
// ... later ...
if err := handle.Wait(); err != nil {
    log.Fatal(err)
}
```

---

### MergeRunners

```go
func MergeRunners(runners ...*Runner) (*Runner, error)
```

Combines multiple runners into one. Use this when a pipeline forks (via [`Partition`](#partition), [`Broadcast`](#broadcast-broadcastn), [`Share`](#share)) into multiple terminal branches that must run together on a shared graph.

```go
valid, invalid := kitsune.Partition(records, isValid)
r1 := valid.ForEach(store).Build()
r2 := invalid.ForEach(logInvalid).Build()
runner, _ := kitsune.MergeRunners(r1, r2)
runner.Run(ctx)
```

---

## :material-puzzle-outline: Stage Composition { #stage-composition }

### Stage[I, O] / Then / Through / Or

```go
type Stage[I, O any] func(*Pipeline[I]) *Pipeline[O]

func Then[I, M, O any](s Stage[I, M], next Stage[M, O]) Stage[I, O]
func (s Stage[I, O]) Apply(p *Pipeline[I]) *Pipeline[O]
func (p *Pipeline[T]) Through(s Stage[T, T]) *Pipeline[T]
func Or[I, O any](primary, fallback func(context.Context, I) (O, error), opts ...StageOption) Stage[I, O]
```

`Stage[I, O]` is a composable pipeline transformer: a function from `*Pipeline[I]` to `*Pipeline[O]`. It lets you name and reuse multi-step pipeline fragments.

[`Then`](#stagei-o-then-through-or) chains two stages: the output of `s` becomes the input of `next`.

`Apply` is syntactic sugar for calling the stage as a function.

[`Through`](#stagei-o-then-through-or) applies a same-type stage to a pipeline inline; useful for chaining stages that preserve the element type.

`Or` creates a `Stage` that tries `primary` and falls back to `fallback` if `primary` returns an error. Both functions are called with the same item.

If both `primary` and `fallback` return errors, the returned error wraps both via `errors.Join` so neither is silently discarded. Both causes are inspectable with `errors.Is` / `errors.As`.

```go
// Define reusable pipeline stages.
var ParseStage kitsune.Stage[string, Event] = func(lines *kitsune.Pipeline[string]) *kitsune.Pipeline[Event] {
    return kitsune.Map(lines, func(ctx context.Context, line string) (Event, error) {
        return parseJSON(line)
    })
}

var EnrichStage kitsune.Stage[Event, EnrichedEvent] = func(events *kitsune.Pipeline[Event]) *kitsune.Pipeline[EnrichedEvent] {
    return kitsune.Map(events, enrich, kitsune.Concurrency(4))
}

// Chain them.
pipeline := kitsune.Then(ParseStage, EnrichStage)
result := pipeline(kitsune.FromSlice(rawLines))

// Or with Through for same-type stages:
normalised := kitsune.FromSlice(rawLines).
    Through(normalize).
    Through(deduplicate)

// Or for fallback:
fetch := kitsune.Or(fetchFromCache, fetchFromDB, kitsune.WithName("fetch"))
```

---

## :material-alert-circle-outline: Error Handling Options { #error-handling }

Error handling is configured per-stage with `OnError(handler)` or pipeline-wide with `WithErrorStrategy(handler)` in run options.

### Halt

```go
func Halt() ErrorHandler
```

Stop the pipeline on the first error. This is the default.

### Skip

```go
func Skip() ErrorHandler
```

Drop the failing item and continue processing subsequent items.

### Return

```go
func Return[T any](val T) ErrorHandler
```

Replace the failed item with `val` and continue. In `FlatMap` stages, behaves like `Skip`.

```go
kitsune.OnError(kitsune.Return(User{Name: "unknown"}))
```

**Type safety caveat:** `ErrorHandler` is not parameterized on the stage's output type. The type parameter `T` on `Return` is inferred from `val` and is not checked against the stage's output type at compile time. If they do not match, the substitution silently fails at runtime: the original error is propagated as though `Halt` has been used. Use a typed variable or prefer `TypedReturn` (see below) for a compile-time guarantee.

`Return` can be composed as a fallback inside `RetryThen` and `RetryIfThen`. `TypedReturn` cannot.

### TypedReturn

```go
func TypedReturn[O any](val O) StageOption
```

A type-safe alternative to `OnError(Return(val))`. The output type `O` is verified at the call site, so a mismatch between `val` and the stage's output type is a compile-time error rather than a silent runtime fallback to `Halt`:

```go
kitsune.Map(orders, fetchUser,
    kitsune.TypedReturn[User](User{Name: "unknown"}),
)
```

`TypedReturn` returns a `StageOption` directly, so it cannot be composed inside `RetryThen` or `RetryIfThen`. For composed retry chains, use `Return` with a typed variable:

```go
var fallback User
kitsune.OnError(kitsune.RetryThen(3, kitsune.FixedBackoff(time.Second), kitsune.Return(fallback)))
```

In `FlatMap` stages, `TypedReturn` behaves like `ActionDrop` because `FlatMap` has no single replacement value to emit.

### RetryMax / RetryThen

```go
func RetryMax(n int, b Backoff) ErrorHandler
func RetryThen(n int, b Backoff, fallback ErrorHandler) ErrorHandler
```

Retry the current item up to `n` times with backoff `b`. `RetryMax` halts after exhausting retries; `RetryThen` delegates to `fallback` (e.g., `ActionDrop()`).

These are error handlers for use with `OnError`; they retry the individual item's transformation function, not the pipeline as a whole. To re-subscribe to an entire upstream source on failure, use the [`Retry`](#retry) operator instead.

### Backoff helpers

```go
func FixedBackoff(d time.Duration) Backoff
func ExponentialBackoff(initial, max time.Duration) Backoff
```

```go
kitsune.OnError(kitsune.RetryThen(3,
    kitsune.ExponentialBackoff(100*time.Millisecond, 5*time.Second),
    kitsune.Skip(),
))
```

### Combining `OnError` and `Supervise`

`OnError` and `Supervise` operate at different levels and can be used together on the same stage. `OnError` is evaluated per item; `Supervise` is evaluated when the stage loop itself crashes. The evaluation order is: `OnError` runs first; only when its final decision is `Halt` (including after retry exhaustion) does `Supervise` see the error and decide whether to restart the stage.

**Stateful stages under Supervise:** For stateful stages (`MapWith`, `MapWithKey`, `FlatMapWith`, `FlatMapWithKey`), per-key `Ref` state is preserved across supervised restarts within a single `Run` call: the key map is allocated once per run and captured by the restarted loop. State is only lost when the surrounding process terminates and a new `Run` starts; for cross-run durability, configure an external Store via `WithStore`.

See the [Error Handling guide](error-handling.md) for the full evaluation model, common combination patterns (retry-then-restart, skip-unless-fatal-then-restart), and observability.

---

## :material-tune: Stage Options Reference { #options }

| Option | Type | Applies to | Description |
|---|---|---|---|
| `Concurrency(n)` | `StageOption` | `Map`, `FlatMap`, `MapWith`, `FlatMapWith`, `MapWithKey`, `FlatMapWithKey`, `ForEach` | Run `n` goroutines in parallel. Default: 1. |
| `Ordered()` | `StageOption` | `Map`, `FlatMap` | Emit results in input order when `Concurrency > 1`. |
| `OnError(h)` | `StageOption` | `Map`, `FlatMap`, `MapWith`, `MapWithKey`, `ForEach`, `DeadLetter`, `CircuitBreaker` | Per-stage error handler. Default: `Halt()`. |
| `Buffer(n)` | `StageOption` | All operators | Channel buffer size between this stage and the next. Default: 16. |
| `Overflow(s)` | `StageOption` | `Map`, `FlatMap`, `Filter`, and most transforms | What to do when the output buffer is full: `Block` (default), `DropNewest`, `DropOldest`. |
| `WithName(s)` | `StageOption` | All operators | Label the stage for metrics, traces, and `Pipeline.Describe()`. |
| `Timeout(d)` | `StageOption` | `Map`, `FlatMap`, `MapWith`, `FlatMapWith` | Per-item deadline. Cancels the item's context after `d`. |
| `Supervise(policy)` | `StageOption` | `Map`, `FlatMap`, `MapWith`, `ForEach` | Restart the stage on error or panic. See `RestartOnError`, `RestartOnPanic`, `RestartAlways`. |
| `BatchTimeout(d)` | `StageOption` | `Batch`, `MapBatch` | Flush a partial batch after `d` even if it is not full. |
| `WithClock(c)` | `StageOption` | `Ticker`, `Timer`, `Batch`, `Throttle`, `Debounce`, `Sample`, `SessionWindow`, `Timestamp`, `TimeInterval` | Substitute a deterministic clock for testing. |
| `CacheBy(keyFn)` | `StageOption` | `Map` only | Enable TTL-based result caching. On a hit, `fn` is skipped. Requires `WithCache` at run time or `CacheBackend`. |
| `WithDedupSet(s)` | `StageOption` | `Dedupe`, `DedupeBy`, `Distinct`, `DistinctBy`, `ExpandMap` | External deduplication backend (Redis, Bloom filter). |
| `VisitedBy(keyFn)` | `StageOption` | `ExpandMap` | Enable cycle detection by key during graph walks. |
| `MaxDepth(n int)` | `StageOption` | `ExpandMap` | Cap BFS depth to `n` levels below roots. `0` = roots only; default unlimited. |
| `MaxItems(n int)` | `StageOption` | `ExpandMap` | Cap total items emitted to `n`. Stage closes normally when cap is hit. Default unlimited. |
| `WithKeyTTL(d)` | `StageOption` | `MapWithKey`, `FlatMapWithKey` | Evict per-key `Ref` entries after `d` of inactivity. 0 disables (default). Overrides `WithDefaultKeyTTL`. |
