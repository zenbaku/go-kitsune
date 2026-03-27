# Go Dataflow Pipeline Library — Design Spec

## Goal

A composable, type-safe dataflow pipeline library for Go. Users write plain Go functions; the library handles channels, goroutines, backpressure, and error routing transparently.

---

## Design Principles

- Users pass **plain functions** — no library types leak into user code
- **Generics** handle type safety; channels are never exposed to users
- **Context-aware** — cancellation propagates through all stages
- **Backpressure-native** — bounded channels, no silent drops
- **Composable** — pipelines are values that can be split, merged, and passed around

---

## The Type Parameter Problem

`Pipeline[T]` represents the type **currently flowing out of the last stage**. This creates a tension with Go's type system:

> **Methods cannot introduce new type parameters in Go.** Only package-level functions can.

This means a fluent chain like `.Map(parseEvent).Filter(isCritical).Map(enrich)` breaks at compile time whenever a stage changes the type.

### Chosen Approach: Option A — Free functions for type-changing transforms

Type-safe throughout. Vertical rather than chained, but every type transition is explicit and compiler-checked.

```go
raw      := pipeline.New(kafkaStream)            // *Pipeline[string]
events   := pipeline.Map(raw, parseEvent)        // *Pipeline[Event]
critical := events.Filter(isCritical).Take(1000) // *Pipeline[Event]  (methods OK, T unchanged)
enriched := pipeline.Map(critical, enrich)        // *Pipeline[Enriched]
batched  := pipeline.Batch(enriched, 100)         // *Pipeline[[]Enriched]
err      := pipeline.ForEach(batched, store).Run(ctx)
```

**Rule of thumb:**
- Free functions when `I → O` (type changes): `Map`, `FlatMap`, `Reduce`, `Batch`
- Methods when `T → T` (type preserved): `Filter`, `Tap`, `Take`, `Skip`, `Dedupe`, `Through`

### Alternative considered: Option D — Compose functions first

Sidesteps the problem by composing type transformations outside the pipeline using a `compose` package, then the pipeline only ever sees one type-safe `Map`. Concurrency options can only be set per composed block, not per individual step.

```go
process := compose.From(parseEvent).Then(enrich).Then(format) // func(string) (Record, error)
err := pipeline.New(raw).Map(process).ForEach(store).Run(ctx)
```

---

## Full API Surface

### Sources

```go
func New[T any](ch <-chan T) *Pipeline[T]
func FromSlice[T any](s []T) *Pipeline[T]
func FromFunc[T any](fn func() (T, bool)) *Pipeline[T]
func FromTicker(d time.Duration) *Pipeline[time.Time]
func Merge[T any](pipelines ...*Pipeline[T]) *Pipeline[T]
```

### Transforms — Free Functions (type may change)

```go
func Map[I, O any](p *Pipeline[I], fn func(I) (O, error), opts ...Option) *Pipeline[O]
func FlatMap[I, O any](p *Pipeline[I], fn func(I) ([]O, error), opts ...Option) *Pipeline[O]
func Reduce[I, O any](p *Pipeline[I], initial O, fn func(O, I) O) *Pipeline[O]
func Batch[T any](p *Pipeline[T], size int, opts ...Option) *Pipeline[[]T]
```

### Transforms — Methods (type preserved)

```go
func (p *Pipeline[T]) Filter(fn func(T) bool, opts ...Option) *Pipeline[T]
func (p *Pipeline[T]) Tap(fn func(T), opts ...Option) *Pipeline[T]
func (p *Pipeline[T]) Through(fn func(*Pipeline[T]) *Pipeline[T]) *Pipeline[T]
func (p *Pipeline[T]) Dedupe(key func(T) any) *Pipeline[T]
func (p *Pipeline[T]) Take(n int) *Pipeline[T]
func (p *Pipeline[T]) Skip(n int) *Pipeline[T]
```

### Fan-out

```go
func Split[T any](p *Pipeline[T], n int) []*Pipeline[T]           // round-robin
func Split2[T any](p *Pipeline[T]) (*Pipeline[T], *Pipeline[T])
func Split3[T any](p *Pipeline[T]) (*Pipeline[T], *Pipeline[T], *Pipeline[T])
func Partition[T any](p *Pipeline[T], fn func(T) bool) (matched *Pipeline[T], rest *Pipeline[T])
func Broadcast[T any](p *Pipeline[T], n int) []*Pipeline[T]       // copy to all
```

### Terminals

```go
func (p *Pipeline[T]) ForEach(fn func(T) error, opts ...Option) *Runner
func (p *Pipeline[T]) Drain(opts ...Option) *Runner

func (r *Runner) Run(ctx context.Context) error
func (r *Runner) RunAsync(ctx context.Context) <-chan error
```

### Collectors (block until done, return materialised result)

```go
func Collect[T any](p *Pipeline[T], ctx context.Context) ([]T, error)
func First[T any](p *Pipeline[T], ctx context.Context) (T, error)
func Last[T any](p *Pipeline[T], ctx context.Context) (T, error)
func Count[T any](p *Pipeline[T], ctx context.Context) (int, error)
func Any[T any](p *Pipeline[T], fn func(T) bool, ctx context.Context) (bool, error)
func All[T any](p *Pipeline[T], fn func(T) bool, ctx context.Context) (bool, error)
func ToChannel[T any](p *Pipeline[T], ctx context.Context) <-chan T
```

### Options (accepted by any stage)

```go
func Concurrency(n int) Option        // parallel workers for this stage
func Buffer(n int) Option             // channel buffer size
func Timeout(d time.Duration) Option  // per-item deadline
func BatchTimeout(d time.Duration) Option // flush partial batch after d
func OnError(h ErrorHandler) Option   // per-stage error policy
func WithHook(h Hook) Option          // observability callbacks
func WithName(name string) Option     // labels stage in metrics/traces
```

### Error Handlers

```go
func Halt() ErrorHandler                                           // stop pipeline (default)
func SkipError() ErrorHandler                                      // log and continue
func SendTo[T any](ch chan<- Error[T]) ErrorHandler                // dead-letter channel
func Retry(n int, b Backoff) ErrorHandler
func RetryWithFallback(n int, b Backoff, h ErrorHandler) ErrorHandler
```

### Backoff

```go
func FixedBackoff(d time.Duration) Backoff
func ExponentialBackoff(initial time.Duration, multiplier float64, max time.Duration) Backoff
func JitteredBackoff(b Backoff) Backoff
```

### Observability Hooks

```go
type Hook interface {
    OnStageStart(name string)
    OnStageEnd(name string, duration time.Duration, err error)
    OnItemIn(stage string)
    OnItemOut(stage string)
    OnItemError(stage string, err error)
}

func PrometheusHook(reg prometheus.Registerer) Hook
func OTelHook(tracer trace.Tracer) Hook
func LogHook(logger *slog.Logger) Hook
func MultiHook(hooks ...Hook) Hook
func NoopHook() Hook
```

---

## Core Types

```go
type Pipeline[T any]   // immutable handle to a typed stage output; chained via free functions or methods
type Runner            // returned by terminals; executes the full graph on Run(ctx)
type Option            // functional option passed to any stage
type ErrorHandler      // pluggable error policy (Halt, SkipError, SendTo, Retry)
type Backoff           // retry backoff strategy
type Hook              // observability callbacks

type Error[T any] struct {
    Value T
    Err   error
    Stage string
}
```

---

## Usage Examples

### Simple linear pipeline

```go
func parse(line string) (LogEntry, error) { ... }
func isCritical(e LogEntry) bool          { return e.Level == "ERROR" }
func notify(e LogEntry) error             { return slack.Send(e.Message) }

lines  := pipeline.FromSlice(rawLines)
parsed := pipeline.Map(lines, parse)
err    := parsed.Filter(isCritical).ForEach(notify).Run(ctx)
```

### Concurrency and batching

```go
raw      := pipeline.New(kafkaStream)
orders   := pipeline.Map(raw, parseOrder)
enriched := pipeline.Map(orders, enrichWithCustomer, pipeline.Concurrency(20))
batched  := pipeline.Batch(enriched, 500, pipeline.BatchTimeout(2*time.Second))
err      := pipeline.ForEach(batched, warehouse.BulkInsert, pipeline.Concurrency(4)).Run(ctx)
```

### Dead-letter error routing

```go
errs   := make(chan pipeline.Error[string], 100)
raw    := pipeline.New(pipeline.FromSlice(urls))
bodies := pipeline.Map(raw, http.Get,
    pipeline.Concurrency(50),
    pipeline.OnError(pipeline.SendTo(errs)))
err    := bodies.Filter(isHTML).ForEach(indexer.Store).Run(ctx)
```

### Fan-out / fan-in

```go
raw               := pipeline.New(rawImages)
thumb, med, large := pipeline.Split3(raw)

thumbs  := pipeline.Map(thumb,  resize(128))
mediums := pipeline.Map(med,    resize(512))
larges  := pipeline.Map(large,  resize(2048))

err := pipeline.Merge(thumbs, mediums, larges).
    ForEach(cdn.Upload).
    Run(ctx)
```

### Reusable middleware fragments via Through

```go
func AuthStage(p *pipeline.Pipeline[Request]) *pipeline.Pipeline[Request] {
    return p.Filter(isAuthenticated).Through(func(p *pipeline.Pipeline[Request]) *pipeline.Pipeline[Request] {
        return pipeline.Map(p, attachUser(authDB))
    })
}

err := pipeline.New(incoming).
    Through(AuthStage).
    Through(RateLimitStage).
    ForEach(handleRequest).
    Run(ctx)
```

---

## Package Structure

```
pipeline/
├── pipeline.go       // Pipeline[T] type, New, FromSlice, FromFunc, FromTicker
├── transforms.go     // Map, FlatMap, Reduce, Batch (free functions)
├── methods.go        // Filter, Tap, Through, Dedupe, Take, Skip (methods)
├── fanout.go         // Split, Split2, Split3, Partition, Broadcast, Merge
├── terminal.go       // ForEach, Drain, Runner, Run, RunAsync
├── collectors.go     // Collect, First, Last, Count, Any, All, ToChannel
├── options.go        // Option, Concurrency, Buffer, Timeout, BatchTimeout, WithName
├── errors.go         // ErrorHandler, Error[T], Halt, SkipError, SendTo, Retry
├── backoff.go        // Backoff, FixedBackoff, ExponentialBackoff, JitteredBackoff
├── hooks.go          // Hook interface, PrometheusHook, OTelHook, LogHook, MultiHook
└── internal/
    ├── graph.go      // DAG, topological sort (Kahn's algorithm), cycle detection
    ├── node.go       // Node[I,O], channel wiring, errgroup lifecycle
    └── chanutil.go   // wrap/unwrap helpers, drain, feed
```

---

## Key Constraints for Implementation

- **Go 1.21+** — requires generics and `slog`
- **No `reflect`** — all type assertions happen at compile time via generics
- **`errgroup`** for stage lifecycle — any error cancels the shared context; all stages drain cleanly
- **Bounded channels throughout** — default buffer `64`, overridable via `Buffer(n)`
- **`Pipeline[T]` is immutable** — every transform returns a new value; the original is unchanged
- **`Runner` is lazy** — no goroutines are started until `Run(ctx)` or `RunAsync(ctx)` is called
