# Kitsune — Design Specification

## Context

Synthesizing two design documents (ChatGPT comprehensive summary + Claude design spec) into the definitive Go pipeline library design. The ChatGPT doc gets the philosophy right (progressive disclosure, intent over infrastructure, ergonomic authoring) but proposes fluent method chaining that is **impossible in Go** due to generic constraints. The Claude doc gets the Go implementation right (free functions for type-changing transforms) but over-scopes v1. This design takes the best of both.

---

## Philosophy

**Kitsune is a type-safe Go pipeline library where users compose ordinary functions with a small set of intentful helpers, while a concurrent, observable runtime handles channels, backpressure, state, and error routing underneath.**

Design principles:
1. Users pass **plain functions** — the library does not leak into user code
2. **Generics** enforce type safety; channels are never exposed
3. **Context-aware** — cancellation propagates through all stages
4. **Backpressure-native** — bounded channels, no silent drops
5. **Progressive disclosure** — simple things are simple, complex things are possible

---

## The Go Type Parameter Constraint

**Methods cannot introduce new type parameters in Go.** This is the single most important constraint shaping the API.

When a stage changes the flowing type (e.g., `string` -> `Event` -> `[]Event` -> `Query`), it must be a **free function**. When the type is preserved (filter, tap, take), it can be a **method**.

This means fluent chains like `From(src).Map(parse).Batch(500)` are impossible. Instead, kitsune uses a **vertical style with named variables**:

```go
lines    := kitsune.FromSlice(rawLines)           // *Pipeline[string]
parsed   := kitsune.Map(lines, parse)              // *Pipeline[Event]
critical := parsed.Filter(isCritical)              // *Pipeline[Event]
batched  := kitsune.Batch(critical, 500)           // *Pipeline[[]Event]
err      := batched.ForEach(store).Run(ctx)
```

**This is a feature, not a limitation.** Each variable name documents what's flowing. The compiler checks every type transition. IDE autocomplete works perfectly.

**Rule of thumb:**
- Free functions when type changes: `Map`, `FlatMap`, `Batch`, `Unbatch`, `MapWith`, `FlatMapWith`
- Methods when type is preserved: `Filter`, `Tap`, `Take`, `Through`, `ForEach`, `Drain`, `Collect`

---

## Core Types

```go
package kitsune

type Pipeline[T any] struct { /* unexported - immutable handle to a stage output */ }
type Runner          struct { /* unexported - lazy executor, no goroutines until Run */ }
type Key[T any]      struct { /* unexported - typed, named state identifier */ }
type Ref[T any]      struct { /* unexported - concurrent-safe state accessor */ }
type StageOption     func(*stageConfig)
type RunOption       func(*runConfig)
type ErrorHandler    /* unexported interface */
type Backoff         func(attempt int) time.Duration
type Store           interface { /* state backend */ }

type Hook interface {
    OnStageStart(ctx context.Context, stage string)
    OnItem(ctx context.Context, stage string, dur time.Duration, err error)
    OnStageDone(ctx context.Context, stage string, processed int64, errors int64)
}
```

---

## API Reference

### Sources

```go
func From[T any](ch <-chan T) *Pipeline[T]
func FromSlice[T any](items []T) *Pipeline[T]
func Generate[T any](fn func(ctx context.Context, yield func(T) bool) error) *Pipeline[T]
func Merge[T any](ps ...*Pipeline[T]) *Pipeline[T]
```

- `From`: wraps an existing channel (e.g., Kafka consumer)
- `FromSlice`: materializes a slice into a stream (great for testing)
- `Generate`: push-based source — call `yield` per item, return when done. `yield` returns false if pipeline is cancelled/done. Handles backpressure internally (blocks when downstream is full)
- `Merge`: fan-in — combines multiple pipelines of the same type into one

### Transforms — Free Functions (type may change)

```go
func Map[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) *Pipeline[O]
func FlatMap[I, O any](p *Pipeline[I], fn func(context.Context, I) ([]O, error), opts ...StageOption) *Pipeline[O]
func Batch[T any](p *Pipeline[T], size int, opts ...StageOption) *Pipeline[[]T]
func Unbatch[T any](p *Pipeline[[]T]) *Pipeline[T]
```

- `Map`: 1:1 transform. The workhorse.
- `FlatMap`: 1:N expansion. Essential for pagination, query fan-out, nested iteration.
- `Batch`: collects N items into a slice. Supports `BatchTimeout` for partial flush.
- `Unbatch`: flattens `[]T` back to individual `T` items. Inverse of `Batch`.

### Transforms — Stateful (type may change, state injected)

```go
func MapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) (O, error), opts ...StageOption) *Pipeline[O]
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) ([]O, error), opts ...StageOption) *Pipeline[O]
```

- Same as `Map`/`FlatMap` but inject a typed `*Ref[S]` for concurrent-safe state access
- The `Ref` handles synchronization and optional external storage
- See **State Management** section below

### Transforms — Methods (type preserved)

```go
func (p *Pipeline[T]) Filter(fn func(T) bool) *Pipeline[T]
func (p *Pipeline[T]) Tap(fn func(T)) *Pipeline[T]
func (p *Pipeline[T]) Take(n int) *Pipeline[T]
func (p *Pipeline[T]) Through(fn func(*Pipeline[T]) *Pipeline[T]) *Pipeline[T]
```

- `Filter`: predicate — pure, no context needed
- `Tap`: side-effect observation (logging, metrics) — does not modify stream
- `Take`: emit first N items, then signal completion
- `Through`: compose reusable type-preserving pipeline fragments

```go
// Through enables reusable middleware:
func Validate(p *kitsune.Pipeline[Order]) *kitsune.Pipeline[Order] {
    return p.Filter(isValid).Tap(logOrder)
}
orders.Through(Validate)
```

### Fan-Out

```go
func Partition[T any](p *Pipeline[T], fn func(T) bool) (match, rest *Pipeline[T])
```

- Routes each item to `match` or `rest` based on the predicate
- Every item goes to exactly one output

### Terminals

```go
func (p *Pipeline[T]) ForEach(fn func(context.Context, T) error, opts ...StageOption) *Runner
func (p *Pipeline[T]) Drain() *Runner
func (p *Pipeline[T]) Collect(ctx context.Context, opts ...RunOption) ([]T, error)
```

- `ForEach`: process each item, return a `Runner` for deferred execution
- `Drain`: consume and discard all items (useful when side-effects happen in Tap/Map)
- `Collect`: convenience — runs the pipeline and materializes all output into a slice

### Runner

```go
func (r *Runner) Run(ctx context.Context, opts ...RunOption) error
```

- **Lazy**: no goroutines start until `Run` is called
- Blocks until the pipeline completes, is cancelled, or encounters an unhandled error
- Returns the first unhandled error, or `ctx.Err()` on cancellation

### Stage Options

```go
func Concurrency(n int) StageOption      // parallel workers (default: 1)
func Buffer(n int) StageOption           // channel buffer size (default: 16)
func WithName(name string) StageOption   // label for metrics/traces/debugging
func OnError(h ErrorHandler) StageOption // per-stage error policy (default: Halt)
func BatchTimeout(d time.Duration) StageOption // flush partial batch after duration
```

### Run Options

```go
func WithStore(s Store) RunOption  // state backend (default: MemoryStore)
func WithHook(h Hook) RunOption    // observability callbacks
```

---

## State Management

### The Problem

Pipelines often need shared mutable state across stages: lookup maps, counters, accumulators. Stages run in separate goroutines, so closure-captured state has data races — even with `Concurrency(1)` per stage, because different stages run concurrently with each other.

### Design: Typed, Declared, Injected, Concurrent-Safe

```go
// 1. Declare a typed key with an initial value
var QueryOrigin = kitsune.NewKey[map[string]Item]("query-origin", make(map[string]Item))

// 2. Access state via injected Ref in stage functions
queries := kitsune.FlatMapWith(enriched, QueryOrigin,
    func(ctx context.Context, ref *kitsune.Ref[map[string]Item], record EnrichedRecord) ([]Query, error) {
        // Atomic read-modify-write
        err := ref.Update(ctx, func(origins map[string]Item) (map[string]Item, error) {
            for _, q := range record.Queries() {
                origins[q.ID] = record.Item
            }
            return origins, nil
        })
        if err != nil {
            return nil, err
        }
        return record.Queries(), nil
    },
)
```

### Key and Ref

```go
func NewKey[T any](name string, initial T) Key[T]

// Ref is injected by the runtime — users never construct one
type Ref[T any] struct { /* unexported */ }
func (r *Ref[T]) Get(ctx context.Context) (T, error)
func (r *Ref[T]) Set(ctx context.Context, value T) error
func (r *Ref[T]) Update(ctx context.Context, fn func(T) (T, error)) error
```

- `Get`: read current value (RLock for memory, GET for Redis)
- `Set`: overwrite value (Lock for memory, SET for Redis)
- `Update`: atomic read-modify-write (Lock for memory, WATCH/MULTI/EXEC for Redis)

### Store Interface

```go
type Store interface {
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, value []byte) error
    Delete(ctx context.Context, key string) error
}

func MemoryStore() Store  // default — optimized to skip serialization
```

- `MemoryStore` is the default. Stores values as `any` internally (no JSON overhead)
- External stores (Redis, DynamoDB) implement the `Store` interface with `[]byte` serialization
- Keys use JSON codec by default for external stores. Custom codecs are a future extension
- State is initialized at `Run` time from the Key's initial value
- Store is configured at `Run` time: `runner.Run(ctx, kitsune.WithStore(redisStore))`

### State Scoping

v1 supports **run-scoped state** only — state lives for the duration of a single `Run` call. Batch-scoped state is a future extension.

---

## Error Handling

```go
func Halt() ErrorHandler                                    // stop pipeline (default)
func Skip() ErrorHandler                                    // drop item, continue
func Retry(n int, b Backoff) ErrorHandler                   // retry n times, then halt
func RetryThen(n int, b Backoff, fallback ErrorHandler) ErrorHandler // retry, then fallback

func FixedBackoff(d time.Duration) Backoff
func ExponentialBackoff(initial, max time.Duration) Backoff
```

Error flow:
1. Stage function returns error
2. Error handler is consulted: Halt? Skip? Retry?
3. **Halt** (default): cancel context, all stages drain, `Run` returns the error
4. **Skip**: log error, drop item, continue processing
5. **Retry**: re-invoke function with same input, up to N times with backoff

Example:
```go
results := kitsune.Map(queries, callExternalAPI,
    kitsune.Concurrency(20),
    kitsune.OnError(kitsune.RetryThen(3, kitsune.ExponentialBackoff(time.Second, 30*time.Second), kitsune.Skip())),
    kitsune.WithName("external-api"),
)
```

---

## Observability

```go
type Hook interface {
    OnStageStart(ctx context.Context, stage string)
    OnItem(ctx context.Context, stage string, dur time.Duration, err error)
    OnStageDone(ctx context.Context, stage string, processed int64, errors int64)
}

func LogHook(logger *slog.Logger) Hook
```

- Hooks are set at run time: `runner.Run(ctx, kitsune.WithHook(hook))`
- `OnStageStart`: called when a stage begins processing
- `OnItem`: called after each item is processed (per item, includes duration and any error)
- `OnStageDone`: called when a stage finishes, with aggregate counts
- `LogHook` provides structured logging via slog
- OTel/Prometheus hooks are future extensions

---

## Helpers

```go
// Lift wraps a context-free function for use with Map/FlatMap
func Lift[I, O any](fn func(I) (O, error)) func(context.Context, I) (O, error)
```

Allows passing plain functions like `strconv.Atoi` (after wrapping):
```go
numbers := kitsune.Map(lines, kitsune.Lift(strconv.Atoi))
```

---

## Internal Architecture

### Pipeline[T] is a Builder

`Pipeline[T]` is a lightweight typed wrapper (~16 bytes) around an untyped graph builder:

```go
type Pipeline[T any] struct {
    graph  *graphBuilder  // shared across all stages in a pipeline
    nodeID int            // which node's output this pipeline represents
}
```

- Immutable: every operation returns a new `Pipeline[O]`
- The graph builder is append-only
- Type safety is enforced at the API boundary; the graph is type-erased internally

### Single-Consumer Rule

Each pipeline output feeds **exactly one** downstream stage. Attempting to use a pipeline as input to two stages panics at `Run` time with a clear error message directing users to `Partition` or `Broadcast`.

This prevents subtle bugs where items are silently distributed between consumers via Go's channel semantics.

### Execution Model

When `Run(ctx)` is called:

1. **Compile**: walk the graph, create bounded channels between nodes, validate topology (no cycles, no dangling outputs, single-consumer check)
2. **Initialize state**: for each Key referenced in the graph, create or load initial state in the configured Store
3. **Launch**: start each node as goroutine(s) in an `errgroup` with a shared cancellable context
4. **Process**: items flow through channels; bounded buffers provide backpressure
5. **Shutdown**: when source exhausts, channels close in topological order; when context is cancelled, all stages drain; `errgroup.Wait()` blocks until all goroutines exit
6. **Return**: first unhandled error, or nil on clean completion

### Concurrency Model

- Default `Concurrency(1)`: stage runs in a single goroutine. Items processed sequentially, output order preserved.
- `Concurrency(n)`: N goroutines read from the input channel. Go distributes items round-robin among readers. **Output order is NOT preserved.** Document this clearly.
- Bounded channels (default buffer 16) provide natural backpressure. If downstream is slow, upstream blocks.

### Node Types (internal)

```go
// All unexported, type-erased
type sourceNode    struct { produce func(ctx, chan<- any) error }
type mapNode       struct { in <-chan any; out chan<- any; fn func(ctx, any) (any, error); cfg stageConfig }
type flatMapNode   struct { in <-chan any; out chan<- any; fn func(ctx, any) ([]any, error); cfg stageConfig }
type filterNode    struct { in <-chan any; out chan<- any; fn func(any) bool }
type batchNode     struct { in <-chan any; out chan<- any; size int; timeout time.Duration }
type partitionNode struct { in <-chan any; match, rest chan<- any; fn func(any) bool }
type sinkNode      struct { in <-chan any; fn func(ctx, any) error; cfg stageConfig }
```

---

## Package Layout

```
go-kitsune/
├── pipeline.go      // Pipeline[T] type, graph builder
├── source.go        // From, FromSlice, Generate, Merge
├── transform.go     // Map, FlatMap, Batch, Unbatch
├── stateful.go      // MapWith, FlatMapWith, Key, Ref, NewKey
├── methods.go       // Filter, Tap, Take, Through
├── fanout.go        // Partition
├── terminal.go      // ForEach, Drain, Collect, Runner
├── option.go        // StageOption, RunOption, all option constructors
├── errors.go        // ErrorHandler, Halt, Skip, Retry, RetryThen, Backoff
├── observe.go       // Hook interface, LogHook
├── store.go         // Store interface, MemoryStore
├── lift.go          // Lift helper
├── internal/
│   ├── graph/
│   │   └── graph.go   // DAG representation, compilation, validation
│   ├── runtime/
│   │   └── exec.go    // errgroup orchestration, channel wiring
│   └── node/
│       └── node.go    // node implementations (map, flatmap, batch, etc.)
├── go.mod
└── go.sum
```

Package declaration: `package kitsune`
Module path: `github.com/jonathan/go-kitsune`

---

## Usage Examples

### Example 1: Simple Linear Pipeline

```go
lines  := kitsune.FromSlice(rawLines)
parsed := kitsune.Map(lines, kitsune.Lift(parseLogEntry))
err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
```

**Concepts used**: FromSlice, Map, Lift, Filter, ForEach, Run (6 concepts)

### Example 2: Concurrent Batched Processing

```go
raw      := kitsune.From(kafkaStream)
orders   := kitsune.Map(raw, parseOrder)
enriched := kitsune.Map(orders, enrichWithCustomer, kitsune.Concurrency(20))
batched  := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
err      := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

### Example 3: Branching with Error Routing

```go
events       := kitsune.Generate(pollEventSource)
parsed       := kitsune.Map(events, parseEvent, kitsune.OnError(kitsune.Skip()))
valid, invalid := kitsune.Partition(parsed, isValid)

stored := valid.ForEach(storeEvent, kitsune.Concurrency(10))
logged := invalid.ForEach(logRejection)

// Merge runners — both branches execute
err := kitsune.MergeRunners(stored, logged).Run(ctx)
```

> **Note**: `MergeRunners` is needed when a pipeline forks into multiple terminal branches. This combines multiple Runners into one that executes all branches. (This is a new concept I'm adding — needed for fan-out to terminals.)

### Example 4: Full Real-World Pipeline (paginated enrichment + correlation)

```go
var QueryOrigin = kitsune.NewKey[map[string]Item]("query-origin", make(map[string]Item))

records   := kitsune.Generate(FetchAllRecords)                                  // *Pipeline[Record]
batched   := kitsune.Batch(records, 500)                                        // *Pipeline[[]Record]
enriched  := kitsune.FlatMap(batched, EnrichBatchWithParents)                   // *Pipeline[EnrichedRecord]
queries   := kitsune.FlatMapWith(enriched, QueryOrigin, BuildQueries)           // *Pipeline[Query]
results   := kitsune.FlatMap(queries, RunExternalSearch,                        // *Pipeline[ExternalResult]
    kitsune.Concurrency(20),
    kitsune.OnError(kitsune.Retry(3, kitsune.ExponentialBackoff(time.Second, 30*time.Second))),
    kitsune.WithName("external-search"),
)
correlated := kitsune.MapWith(results, QueryOrigin, CorrelateResult)            // *Pipeline[FinalResult]

err := correlated.ForEach(SendToSQS, kitsune.Concurrency(10)).
    Run(ctx, kitsune.WithHook(kitsune.LogHook(slog.Default())))
```

Each line reads as business intent: fetch, batch, enrich, query, search, correlate, send. The type comments show the data shape transforming at each step.

### Example 5: Testing

```go
func TestEnrichment(t *testing.T) {
    input := kitsune.FromSlice([]Record{{ID: "1"}, {ID: "2"}})
    enriched := kitsune.Map(input, enrich)

    results, err := enriched.Collect(context.Background())
    require.NoError(t, err)
    require.Len(t, results, 2)
    assert.Equal(t, "enriched-1", results[0].Name)
}
```

`FromSlice` + `Collect` makes every pipeline fully testable with zero infrastructure.

---

## v1 Exported Symbol Count

| Category | Symbols | Count |
|----------|---------|-------|
| Types | Pipeline, Runner, Key, Ref, Store, Hook, StageOption, RunOption, ErrorHandler, Backoff | 10 |
| Sources | From, FromSlice, Generate, Merge | 4 |
| Transforms | Map, FlatMap, Batch, Unbatch, MapWith, FlatMapWith | 6 |
| Methods | Filter, Tap, Take, Through, ForEach, Drain, Collect | 7 |
| Fan-out | Partition | 1 |
| Runner | Run, MergeRunners | 2 |
| Options | Concurrency, Buffer, WithName, OnError, BatchTimeout, WithStore, WithHook | 7 |
| Errors | Halt, Skip, Retry, RetryThen, FixedBackoff, ExponentialBackoff | 6 |
| State | NewKey, MemoryStore | 2 |
| Ref methods | Get, Set, Update | 3 |
| Hooks | LogHook | 1 |
| Helpers | Lift | 1 |
| **Total** | | **50** |

~50 symbols, but only **~15 concepts**. A user building their first pipeline needs exactly 4: `FromSlice`, `Map`, `ForEach`, `Run`. Progressive disclosure handles the rest.

---

## v2 Roadmap (not in scope, but designed-for)

- `FromIter(iter.Seq[T])` — Go iterator integration
- `Broadcast(p, n)` — copy to all N consumers
- `RunAsync(ctx) <-chan error` — non-blocking execution
- `First`, `Last`, `Count`, `Any`, `All` — collectors
- `Dedupe(key)`, `Skip(n)` — additional filters
- `Window(duration)` — time-based batching
- `Ordered()` — preserve output order with Concurrency(n)
- Circuit breaker, rate limiting
- OTel/Prometheus hook implementations
- Redis/DynamoDB Store implementations
- Live inspector web UI (runtime event stream)
- Batch-scoped state

---

## Key Design Decisions & Rationale

### 1. Vertical style over fluent chains
Go generics make fluent chains with type changes impossible. The vertical style with named variables is not a compromise — it's clearer. Variable names document data flow. Types are visible. This is idiomatic Go.

### 2. Context in all stage functions
Every `Map`/`FlatMap`/`ForEach` function receives `context.Context`. This matches Go convention, enables cancellation, timeouts, and tracing. The `Lift` helper covers the simple case.

### 3. Typed state with Ref over closure-captured state
Even with `Concurrency(1)` per stage, stages run concurrently with each other. A map written in stage A and read in stage C is a data race. `Ref[T]` provides concurrent-safe access with an API that works for both in-memory and external stores.

### 4. Store interface with []byte
Keeps the interface implementable for any backend. MemoryStore is special-cased internally to avoid serialization overhead. The indirection cost is zero for the common case.

### 5. Single-consumer rule
Prevents subtle item-distribution bugs. Users who need multiple consumers use explicit `Partition` (predicate-based routing) or future `Broadcast` (copy to all). Fails fast at Run time with a helpful error.

### 6. Backpressure via bounded channels
Default buffer of 16 items per stage. If downstream is slow, upstream blocks automatically. No configuration needed for correct behavior. `Buffer(n)` allows tuning for throughput vs latency.

### 7. Lazy execution via Runner
The pipeline is a static description. No goroutines start until `Run(ctx)`. This allows inspection, validation, and testing of pipeline structure before execution.

### 8. MergeRunners for fan-out terminals
When a pipeline forks (via `Partition`), each branch may have its own terminal. `MergeRunners` combines them into a single `Runner` that executes all branches. This avoids requiring users to manage multiple `Run` calls.

---

## Implementation Order

1. **Core types**: `Pipeline[T]`, graph builder, node types
2. **Sources**: `FromSlice`, `Generate` (enough for testing)
3. **Map + ForEach + Runner**: minimal working pipeline
4. **Filter, Tap, Take**: type-preserving methods
5. **FlatMap, Batch, Unbatch**: shape-changing transforms
6. **Error handling**: Halt, Skip, Retry
7. **Concurrency**: multi-worker stages, Buffer option
8. **State**: Key, Ref, MemoryStore, MapWith, FlatMapWith
9. **Fan-out**: Partition, MergeRunners, Merge
10. **Observability**: Hook interface, LogHook
11. **Through, Lift**: composition helpers
12. **From** (channel source): production source adapter

Each step produces a working, testable increment.
