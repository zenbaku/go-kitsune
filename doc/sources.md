# Source Selection Guide

Sources are the entry points of a Kitsune pipeline. Fifteen source functions exist,
each suited to a different situation. This guide answers "which source do I use?" so
you do not have to read every godoc in sequence.

---

## Quick decision table

| Situation | Source |
|---|---|
| Items already in memory | `FromSlice` |
| Existing `<-chan T` you do not own | `From` |
| Pull-based loop (paged API, cursor, polling) | `Generate` |
| Push-based external senders (HTTP handlers, gRPC streams) | `NewChannel` / `Channel[T]` |
| Go 1.23 iterator (`iter.Seq[T]`) | `FromIter` |
| Emit on a wall-clock interval | `Ticker` |
| Emit one value after a delay | `Timer` |
| Mathematical sequence with explicit state | `Unfold` |
| Mathematical sequence with implicit state | `Iterate` |
| Infinite stream from a function call | `Repeatedly` |
| Infinite repeating cycle of a fixed list | `Cycle` |
| Sequential chaining of pipeline segments | `Concat` |
| Race multiple sources, use whoever fires first | `Amb` |
| Identity element for composition (produces nothing) | `Empty[T]()` |
| Absorbing element / placeholder that never emits | `Never[T]()` |

---

## Sources explained

### `FromSlice[T](items []T)` — in-memory data

Use when you have all the data already in a slice. This is the most common source
in tests and the right choice for any finite in-memory collection.

```go
p := kitsune.FromSlice([]string{"alice", "bob", "carol"})
```

### `From[T](src <-chan T)` — wrap an existing channel

Use when another part of your program already owns and populates a channel and you
want to pull items from it into a pipeline. The pipeline completes when the channel
is closed. You remain responsible for closing the channel.

```go
ch := make(chan Event, 64)
go producer(ch)
p := kitsune.From(ch)
```

**Do not use** `From` when you need to push items from multiple goroutines after
pipeline construction — use `NewChannel` instead.

### `Generate[T](fn)` — pull-based producer loop

Use when the producer is a loop you control: paginated APIs, database cursors,
file readers, polling loops. `Generate` calls your function with a `yield` callback;
call `yield(item)` for each item and return when done. `yield` returns false when
downstream is done (e.g. after `Take`), so check its return value.

```go
p := kitsune.Generate(func(ctx context.Context, yield func(Page) bool) error {
    cursor := ""
    for {
        page, next, err := api.ListPages(ctx, cursor)
        if err != nil {
            return err
        }
        if !yield(page) {
            return nil // downstream stopped
        }
        if next == "" {
            return nil // no more pages
        }
        cursor = next
    }
})
```

**Generate vs NewChannel:** prefer `Generate` when the producer is a loop you can
write inline. Prefer `NewChannel` when items arrive from external goroutines that
the pipeline does not own (HTTP handlers, callbacks).

### `NewChannel[T](buffer int)` / `Channel[T]` — push-based multi-sender bridge

Use when items arrive asynchronously from goroutines you do not control. Create a
`Channel[T]`, call `.Source()` once to get the pipeline, then send items with
`.Send(ctx, item)` from any goroutine. Call `.Close()` when no more items will arrive.

```go
ch := kitsune.NewChannel[Event](32)
p := ch.Source()

// In HTTP handler goroutines:
go func() { ch.Send(ctx, event) }()

// Shut down:
ch.Close()
```

`Channel` is safe for concurrent use. `Send` blocks under backpressure; `TrySend`
returns false instead of blocking.

### `FromIter[T](seq iter.Seq[T])` — Go 1.23 iterators

Use when you have a standard library or third-party iterator (e.g. `slices.Values`,
`maps.Keys`, a database row iterator). Bridges the Go iterator protocol into a pipeline.

```go
p := kitsune.FromIter(slices.Values(mySlice))
```

### `Ticker(d time.Duration)` — wall-clock interval

Use when you need to do something on a repeating schedule (heartbeats, polling,
periodic flushes). Emits `time.Time` values at each tick. Infinite: pair with
`Take(n)` or `TakeWhile` to bound it.

```go
// Emit a tick every second, take 10 ticks.
p := kitsune.Ticker(time.Second).Take(10)
```

### `Timer[T](delay, fn)` — single value after a delay

Use when you need exactly one item emitted after a fixed delay. Completes after
the single emission.

```go
// Emit a "timeout" sentinel after 30 seconds.
p := kitsune.Timer(30*time.Second, func() string { return "timeout" })
```

### `Unfold[S, T](seed, fn)` — explicit-state mathematical sequence

Use for sequences where the next value depends on a state that changes at each step.
`fn` receives the current state and returns `(value, nextState, stop)`. Unlike
`Iterate`, the state type `S` can differ from the value type `T`.

```go
// Fibonacci: state is [2]int{a, b}, value is a.
p := kitsune.Unfold([2]int{0, 1}, func(s [2]int) (int, [2]int, bool) {
    return s[0], [2]int{s[1], s[0] + s[1]}, false
}).Take(8)
// → 0, 1, 1, 2, 3, 5, 8, 13
```

### `Iterate[T](seed, fn)` — implicit-state mathematical sequence

Use for sequences where the next value is a simple function of the previous value
and the value and state are the same type.

```go
p := kitsune.Iterate(1, func(n int) int { return n * 2 }).Take(5)
// → 1, 2, 4, 8, 16
```

### `Repeatedly[T](fn)` — infinite stream from a function

Use when every item is produced by calling the same function repeatedly (random
number generation, reading from a ring buffer, calling a sensor). Infinite: pair
with `Take(n)` or `TakeWhile`.

```go
p := kitsune.Repeatedly(rand.Int).Take(100)
```

### `Cycle[T](items)` — infinite repeating list

Use when you want to loop over a fixed set of values indefinitely (round-robin
selection, test fixtures). Panics on empty input. Infinite: pair with `Take(n)`.

```go
p := kitsune.Cycle([]string{"red", "green", "blue"}).Take(7)
// → red, green, blue, red, green, blue, red
```

### `Concat[T](factories...)` — sequential pipeline chaining

Use when you need to run pipelines one after another: all items from the first
complete before the second starts. Factories are functions rather than `*Pipeline`
values because each pipeline is a live graph; a factory lets `Concat` construct
each graph fresh when needed.

```go
kitsune.Concat(
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
)
// → 1, 2, 3, 4
```

### `Amb[T](factories...)` — race multiple sources

Use when you have multiple possible sources and want items from whichever responds
first. All factories start concurrently; as soon as one emits its first item, all
others are cancelled. Classic use: primary + replica database reads, multi-region
service calls.

```go
kitsune.Amb(
    func() *kitsune.Pipeline[Result] { return fetchFromPrimary(ctx) },
    func() *kitsune.Pipeline[Result] { return fetchFromReplica(ctx) },
)
```

### `Empty[T]()` — identity element (produces nothing)

Completes immediately with no items. Use as a base case in tests, a placeholder
in conditional pipeline construction, or the identity element in composition:
`Merge(Empty[T](), p)` behaves identically to `p`.

```go
var src *kitsune.Pipeline[Event]
if condition {
    src = realSource()
} else {
    src = kitsune.Empty[Event]()
}
```

### `Never[T]()` — absorbing element (blocks forever)

Never emits any items and never completes until the context is cancelled. Use as a
placeholder in tests that assert on other branches, or as the identity element for
`Amb`: `Amb(Never[T](), p)` emits whatever `p` emits.

```go
// In a test: assert that the error branch fires before any items arrive.
p := kitsune.Amb(
    func() *kitsune.Pipeline[int] { return kitsune.Never[int]() },
    func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1}) },
)
```

---

## Generate vs NewChannel — extended comparison

This distinction trips up most new users.

| | `Generate` | `NewChannel` |
|---|---|---|
| **Pull vs push** | Pull: the pipeline drives iteration | Push: external code sends at will |
| **Producer location** | Inline loop inside the factory function | Any goroutine, any time |
| **Backpressure** | `yield` blocks automatically | `Send` blocks; `TrySend` does not |
| **Shutdown** | Return from the function | Call `Close()` |
| **Error propagation** | Return an error from the function | Not supported (use `OnError` at the stage level) |
| **Best for** | Paged APIs, cursors, polling | HTTP handlers, gRPC streams, callbacks, fan-in from many goroutines |

Rule of thumb: if you can write the producer as a `for` loop that calls `yield`,
use `Generate`. If items arrive from goroutines you do not control, use `NewChannel`.
