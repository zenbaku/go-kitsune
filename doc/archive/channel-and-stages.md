# Channel[T] and Stage[I,O] — Design Specification

Two related features that unlock programmatic pipeline feeding and composable pipeline architecture.

---

## Motivation

**Channel[T]** solves a source problem: every current source (`FromSlice`, `Generate`, `From`) is self-contained — the source fully controls when items arrive. Real systems are the inverse: external events arrive (HTTP requests, CLI input, tests, event loops) and the pipeline should process them. Today the workaround is a raw `chan T` via `From(ch)`, which has footguns (sending to closed channel panics, forgetting `close` hangs the pipeline forever) and forces manual goroutine management because `Run` blocks.

**Stage[I,O]** solves an architecture problem: reusable pipeline fragments exist today as plain Go functions (`func(*Pipeline[I]) *Pipeline[O]`), but there is no named type for this shape, no combinator to chain them, and no idiomatic way to store or pass them. Large pipelines end up as monolithic build functions that are hard to test in isolation or reuse across different contexts.

Together they enable a **service pipeline pattern**: define stages once, reuse them with a `Channel[T]` source in production and a `FromSlice` source in tests — identical operator graph, swappable source.

---

## Feature 1: `Channel[T]` + `RunAsync`

### API

```go
// NewChannel creates a push-based source with an internal buffer of the given size.
// The buffer decouples producers from the pipeline's processing rate.
func NewChannel[T any](buffer int) *Channel[T]

// Source returns the *Pipeline[T] for this channel. Panics if called more than
// once (single-consumer rule — use Broadcast if multiple consumers are needed).
func (c *Channel[T]) Source() *Pipeline[T]

// Send pushes an item into the channel. Blocks if the buffer is full (backpressure).
// Returns an error if the context is cancelled or the channel is already closed.
func (c *Channel[T]) Send(ctx context.Context, item T) error

// TrySend pushes an item without blocking.
// Returns false if the buffer is full; the item is not enqueued.
func (c *Channel[T]) TrySend(item T) bool

// Close signals that no more items will be sent.
// Safe to call multiple times (idempotent). The pipeline drains and exits cleanly
// after all buffered items are processed.
func (c *Channel[T]) Close()

// RunAsync starts the pipeline in a background goroutine and returns a channel
// that receives exactly one value: nil on clean completion, or the first error.
// It is the non-blocking counterpart to Runner.Run.
func (r *Runner) RunAsync(ctx context.Context, opts ...RunOption) <-chan error
```

### Implementation notes

- `Channel[T]` wraps a `chan T` internally; `Source()` delegates to the existing `From(ch)` source node — no engine changes required.
- `Close()` uses `sync.Once` to prevent double-close panics.
- `Send` returns `ctx.Err()` if the context is cancelled while waiting for buffer space, and a sentinel `ErrChannelClosed` if `Close()` has already been called.
- `RunAsync` is `go func() { errCh <- r.Run(ctx, opts...) }()` — a thin wrapper; no engine changes required.

### Usage

```go
// Production: push from HTTP handlers
src   := kitsune.NewChannel[RawEvent](256)
p     := src.Source()
out   := kitsune.Map(p, parse, kitsune.Concurrency(10))
errCh := out.ForEach(store).RunAsync(ctx)

http.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
    if err := src.Send(r.Context(), readBody(r)); err != nil {
        http.Error(w, err.Error(), 500)
    }
})
<-errCh // block until pipeline finishes (e.g. on server shutdown)

// Test: inject specific items, deterministic
out, _ := kitsune.Map(kitsune.FromSlice(testEvents), parse).Collect(ctx)
```

### Backpressure behaviour

| Method    | Buffer full behaviour |
|-----------|----------------------|
| `Send`    | Blocks until space available or context cancelled |
| `TrySend` | Returns `false` immediately; item is dropped |

---

## Feature 2: `Stage[I, O]` + `Then`

### API

```go
// Stage[I, O] is a reusable, composable pipeline transformation.
// A Stage is a first-class value that can be defined, stored, passed as an
// argument, tested in isolation, and combined with other stages via Then.
//
// A Stage is a function — define one with a direct type conversion:
//
//   var ParseStage Stage[string, Event] = func(p *Pipeline[string]) *Pipeline[Event] {
//       return kitsune.Map(p, parseEvent, kitsune.Concurrency(5))
//   }
type Stage[I, O any] func(*Pipeline[I]) *Pipeline[O]

// Apply runs this stage against an input pipeline, returning the output pipeline.
func (s Stage[I, O]) Apply(p *Pipeline[I]) *Pipeline[O]

// Then composes two stages into a single stage that runs first, then second.
//
// Must be a free function: Go methods cannot introduce new type parameters,
// so s1.Then(s2) is impossible when the output type of s1 differs from I.
// For long chains, name intermediate compositions to keep code readable.
//
//   var ParseAndEnrich = kitsune.Then(ParseStage, EnrichStage)
//   var Full           = kitsune.Then(ParseAndEnrich, FormatStage)
func Then[A, B, C any](first Stage[A, B], second Stage[B, C]) Stage[A, C]
```

### Compatibility with `.Through()`

`Stage[T, T]` (type-preserving) has exactly the signature `.Through()` expects, so they are directly interchangeable. No adapter needed:

```go
var ValidateStage Stage[Order, Order] = func(p *Pipeline[Order]) *Pipeline[Order] {
    return p.Filter(isValid).Tap(logOrder)
}

orders.Through(ValidateStage) // existing Through API
ValidateStage.Apply(orders)   // Stage API — identical result
```

### Implementation notes

- `Stage[I, O]` is a `type` declaration over a function type — zero runtime cost.
- `Apply` is a one-line method delegating to the underlying function.
- `Then` is a three-line free function creating a closure. No engine changes required.
- All three additions go in a new file `stage.go` in the root package.

### Usage

```go
// Define stages as package-level variables or functions that return Stage values:
var ParseStage  Stage[string, Event]         = func(p *Pipeline[string]) *Pipeline[Event]         { ... }
var EnrichStage Stage[Event, EnrichedEvent]  = func(p *Pipeline[Event]) *Pipeline[EnrichedEvent]  { ... }
var FormatStage Stage[EnrichedEvent, Result] = func(p *Pipeline[EnrichedEvent]) *Pipeline[Result] { ... }

// Compose two stages:
var ParseAndEnrich = kitsune.Then(ParseStage, EnrichStage)       // Stage[string, EnrichedEvent]

// Compose three (name intermediate to avoid deep nesting):
var FullPipeline   = kitsune.Then(ParseAndEnrich, FormatStage)   // Stage[string, Result]

// Apply to any source:
result := FullPipeline.Apply(source)

// Test each stage independently:
events, _ := ParseStage.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
enriched, _ := EnrichStage.Apply(kitsune.FromSlice(events)).Collect(ctx)
```

### Parameterised (struct-based) stages

The library provides no interface for struct-based stages — the Go pattern is sufficient. A struct with an `.AsStage()` method is the convention:

```go
type EnrichmentStage struct {
    Concurrency int
    Cache       kitsune.Cache
    TTL         time.Duration
}

func (s *EnrichmentStage) AsStage() kitsune.Stage[Event, EnrichedEvent] {
    return func(p *kitsune.Pipeline[Event]) *kitsune.Pipeline[EnrichedEvent] {
        return kitsune.Map(p, s.enrich,
            kitsune.CacheBy(eventKey, kitsune.CacheBackend(s.Cache), kitsune.CacheTTL(s.TTL)),
            kitsune.Concurrency(s.Concurrency))
    }
}

// Usage:
enrich := (&EnrichmentStage{Concurrency: 20, Cache: cache, TTL: 5 * time.Minute}).AsStage()
full   := kitsune.Then(ParseStage, enrich)
```

### Multi-output stages

`Stage[I, O]` is intentionally 1-in-1-out. Multi-output operations (`Partition`, `Broadcast`) return multiple `*Pipeline[T]` values and cannot be generalised into the `Stage` type without losing type safety. The convention is a plain function:

```go
// A "split" — not Stage[I,O], just a function returning two pipelines:
func RouteOrders(p *Pipeline[Order]) (highValue, regular *Pipeline[Order]) {
    return kitsune.Partition(p, func(o Order) bool { return o.Amount > 1000 })
}

// Wire manually — the compiler still checks every type:
high, regular := RouteOrders(source)
vipResult     := VIPStage.Apply(high)
stdResult     := StandardStage.Apply(regular)
```

### The `Then` chaining trade-off

Go methods cannot introduce new type parameters, so `s1.Then(s2)` is impossible when the types change across stages. This is a language constraint, not a kitsune choice. The nested free-function form is the current solution:

```go
// Verbose for long chains:
kitsune.Then(a, kitsune.Then(b, kitsune.Then(c, d)))

// Preferred: name intermediate compositions
var AB  = kitsune.Then(a, b)
var CD  = kitsune.Then(c, d)
var Full = kitsune.Then(AB, CD)
```

A future Go generalisation of type-parameterised methods could enable `a.Then(b).Then(c)` syntax. Until then, named intermediates are the idiomatic workaround.

---

## Combining both features

```go
// Define stages once — independently testable, reusable across services:
var IngestionStage  = kitsune.Then(ParseStage, ValidateStage)  // Stage[string, Event]
var ProcessingStage = kitsune.Then(EnrichStage, FormatStage)   // Stage[Event, Result]
var FullPipeline    = kitsune.Then(IngestionStage, ProcessingStage) // Stage[string, Result]

// Production: long-lived service, push from outside
src   := kitsune.NewChannel[string](256)
errCh := FullPipeline.Apply(src.Source()).ForEach(store).RunAsync(ctx)

go func() {
    for event := range externalStream {
        src.Send(ctx, event)
    }
    src.Close()
}()

if err := <-errCh; err != nil { log.Fatal(err) }

// Test: deterministic, no goroutines needed
results, _ := FullPipeline.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
```

---

## Files to create / modify

| File | Change |
|---|---|
| `source.go` | Add `Channel[T]`, `NewChannel`, `(c).Source()`, `(c).Send`, `(c).TrySend`, `(c).Close` |
| `kitsune.go` | Add `(r).RunAsync` |
| `stage.go` (new) | Add `Stage[I,O]` type, `(s).Apply`, `Then[A,B,C]` |
| `kitsune_test.go` | Tests for Channel (send, backpressure, close, TrySend), RunAsync, Stage (Apply, Then, Through compat, struct pattern, test isolation) |
| `examples/channel/` (new) | `Channel[T]` + `RunAsync`: simulated event stream fed from a goroutine |
| `examples/stages/` (new) | `Stage[I,O]` + `Then`: multi-stage pipeline defined as composable units, tested independently |
| `README.md` | Add `Channel`, `RunAsync`, `Stage`, `Then` to operator catalog |
