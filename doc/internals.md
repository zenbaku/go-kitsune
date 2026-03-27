# Kitsune Internals

This document explains how Kitsune works under the hood — the data structures,
concurrency model, and runtime machinery that powers every pipeline. It is
aimed at contributors and at users who want a mental model deeper than the
public API.

---

## The big picture

A Kitsune pipeline is a **directed acyclic graph (DAG)** of processing stages.
That graph is assembled lazily during pipeline construction — no goroutines
start, no memory is allocated for channels — and then materialised at a single
point in time when `Run` (or `Collect`) is called.

```
Construction time                    Run time
─────────────────────────────────    ──────────────────────────────────────
FromSlice → Map → Filter → ForEach   validate → wire channels → launch goroutines
       (Graph object grows)                     (execution begins)
```

The public kitsune package is a thin generic wrapper. All values flow through
the runtime as `any`; the generics only exist at the API boundary, erased to
concrete types on entry and restored via type assertion on exit.

---

## The Graph and Node

```
internal/engine/graph.go
```

**`Graph`** is a thread-safe append-only list of `*Node` values. It is created
once per pipeline (by the first source call) and shared by reference through
every `*Pipeline[T]` handle. The `mu` mutex only protects `AddNode` during
construction; at run time the graph is read-only.

**`Node`** is the central configuration record for one stage:

```
Node {
    ID           int          // position in g.Nodes; also the channel namespace
    Kind         NodeKind     // dispatch key (Source, Map, Filter, …)
    Name         string       // optional user label; used in hooks and logs
    Fn           any          // type-erased processing function

    Inputs       []InputRef   // upstream (node, port) pairs this stage reads from
    Concurrency  int          // number of workers (default 1)
    Ordered      bool         // preserve input order when Concurrency > 1
    Buffer       int          // output channel capacity (default 16)
    Overflow     int          // 0=Block, 1=DropNewest, 2=DropOldest
    ErrorHandler ErrorHandler // retry / skip / halt policy
    Supervision  SupervisionPolicy

    // Kind-specific fields:
    BatchSize    int
    BatchTimeout int64        // nanoseconds
    BatchConvert func([]any) any
    TakeN        int
    BroadcastN   int
    ZipConvert   func(any, any) any
}
```

An `InputRef` is just `{Node int, Port int}` — a pointer into another node's
output. `Port` is almost always 0; the exceptions are `Partition` (ports 0 and
1 for the two branches) and `Broadcast` (ports 0…N-1).

`ChannelKey` pairs a node ID with a port number and serves as the map key for
both the channel and the outbox maps that are built at run time.

---

## Channel wiring

```
internal/engine/compile.go: CreateChannels, CreateOutboxes
```

At the start of `Run`, `CreateChannels` allocates one `chan any` per output
port of every non-sink node. The result is a `map[ChannelKey]chan any`.

```
Node 0 (Source)    → chan[{0,0}]  ──→  Node 1 (Map)
Node 1 (Map)       → chan[{1,0}]  ──→  Node 2 (Filter)
Node 2 (Filter)    → chan[{2,0}]  ──→  Node 3 (Sink)
```

Multi-port nodes get one channel per port:

```
Partition  →  chan[{n,0}]  (match branch)
           →  chan[{n,1}]  (rest branch)

Broadcast  →  chan[{n,0}]  …  chan[{n,k-1}]  (k consumer branches)
```

`CreateOutboxes` wraps each channel in an `Outbox`, applying the overflow
strategy configured on that node (see next section).

---

## Outboxes and overflow

```
internal/engine/outbox.go
```

The `Outbox` interface has two methods:

```go
Send(ctx context.Context, item any) error
Dropped() int64
```

Every stage writes to its output exclusively through its `Outbox`, never to the
raw channel directly. This indirection is where the three overflow strategies
diverge:

**Block (default)** — the trivial implementation. A simple `select` that either
sends the item or returns `ctx.Err()` if the context is cancelled while
waiting. Zero overhead, no bookkeeping.

**DropNewest** — a non-blocking try-send. If the buffer is full, the *incoming*
item is discarded. A thread-safe atomic counter records the drop, and
`OverflowHook.OnDrop` is called if the hook implements it. No locks.

**DropOldest** — evicts the oldest buffered item to make room. The fast path
(buffer has space) is identical to Block: no locks. The slow path (buffer
full) takes a mutex, reads one item from the channel to free a slot, then
writes the new item. The mutex is only held during that eviction, so normal
sends remain contention-free.

All three implementations are instantiated by `NewOutbox` in `compile.go`,
which selects the right type based on `n.Overflow`.

---

## How Run ties it all together

```
internal/engine/run.go: Run
```

```
Run(ctx, g, cfg)
  │
  ├── Validate(g)          single-consumer check, sink-present check
  ├── g.InitRefs(store)    materialise state keys
  ├── CreateChannels(g)
  ├── CreateOutboxes(g, chans, hook)
  ├── notify BufferHook    pass query closure over chans map
  ├── make done chan        early-exit signal (Take / TakeWhile)
  │
  └── if cfg.DrainTimeout > 0
  │     runWithDrain(...)
  └── else
        eg, egCtx := errgroup.WithContext(ctx)
        for each node:
            eg.Go(nodeRunner(egCtx, n, ...))
        return eg.Wait()
```

Each stage is a separate goroutine. The errgroup provides a shared cancellation
context (`egCtx`): when any goroutine returns a non-nil error, `egCtx` is
cancelled and the other goroutines see it on their next `ctx.Done()` check.

**`nodeRunner`** is a closure factory. It resolves the node's input channels
from the channel map, builds an `outCloser` that closes the output channel(s)
when the stage finishes, selects the right `runXxx` implementation, wraps
everything in `supervise`, and returns a `func() error` for the errgroup:

```
nodeRunner returns func() error {
    defer outCloser()
    return supervise(ctx, policy, hook, name, inner)
}
```

Closing the output channel on exit is how downstream stages learn the stream
is exhausted — they see `item, ok := <-inCh; ok == false` and exit cleanly.

---

## The done channel: early exit without context cancellation

When `Take` or `TakeWhile` decides no more items are needed, it must tell the
upstream source to stop producing. Simply cancelling the context would also
stop downstream stages that are still processing in-flight items — not what we
want.

Instead, there is a separate `done chan struct{}` that is closed by
`signalDone()` (a `sync.Once` wrapper). Sources poll it on every item in their
yield callback:

```go
select {
case <-done:   return false   // stop producing
case <-ctx.Done(): return false
default:
}
```

`runSource` takes this a step further: it creates a `srcCtx` derived from `ctx`
that is *also* cancelled when `done` fires, so a source function that parks on
`<-ctx.Done()` (e.g., to simulate an infinite stream) wakes up correctly on
a drain signal without needing to know about `done` directly:

```
parentCtx ──derives──▶ srcCtx  ◀── cancelled when done fires
                           │
                     passed to source fn
```

When `done` causes `srcCtx` to cancel, `runSource` suppresses the resulting
`ctx.Err()` (treating it as a clean exit) because the source stopping was
deliberate.

---

## Concurrency patterns inside a stage

### Single worker (default)

Most stages run with `Concurrency: 1`. The inner loop is a simple
`for { select { case item := <-inCh: … case <-ctx.Done(): } }`. No
synchronisation overhead beyond the channel itself.

### Concurrent unordered (`Concurrency(n)`)

`runMapConcurrent` spawns `n` worker goroutines and lets them race:

```
inCh ──▶ [worker 0] ──▶
         [worker 1] ──▶  outbox (shared, thread-safe)
         [worker 2] ──▶
```

Error coordination uses an `errOnce`/`firstErr`/`innerCancel` triple:
when any worker hits an error it atomically records it (only the first error
survives), then calls `innerCancel()` to stop the other workers via their
context. The caller waits on a `sync.WaitGroup` and returns `firstErr`.

### Concurrent ordered (`Concurrency(n)` + `Ordered()`)

`runMapConcurrentOrdered` preserves input order using a slot pipeline:

```
                    jobs chan
inCh ──▶ dispatcher ──────────▶ [worker 0]
         (reads inCh,          [worker 1]   each worker fills its slot
          allocates slots,     [worker 2]
          enqueues to pending)

         pending chan                collector
         (slot pointers) ──────────▶ (reads pending in order,
                                       waits for each slot.done,
                                       sends result to outbox)
```

A *slot* is `{result any; err error; done chan struct{}}`. The dispatcher
creates one slot per item, sends the slot to both `jobs` (for a worker to
fill) and `pending` (to maintain order). Workers process concurrently but
the collector always drains `pending` in arrival order — it blocks on
`<-slot.done` before emitting each result.

---

## Fan-out: Partition and Broadcast

**`runPartition`** evaluates a predicate and routes each item to one of two
outboxes (port 0 = match, port 1 = rest). It is a single loop; every item
goes to exactly one branch.

**`runBroadcast`** sends every item to all N outboxes in a sequential loop.
Because the sends are sequential, a slow consumer on one branch
backpressures the whole broadcast. Size buffers generously on broadcast
branches if the consumers run at different speeds.

Both nodes close all of their output channels when they exit, which cascades
shutdown down every branch.

---

## Fan-in: Merge

`runMerge` spawns one goroutine per input channel. All goroutines write to the
*same* shared outbox, relying on its thread-safety. Errors are captured with
the same `errOnce`/`innerCancel` pattern as the concurrent map:

```
inCh[0] ──▶ goroutine 0 ──▶
inCh[1] ──▶ goroutine 1 ──▶  shared outbox → outCh
inCh[k] ──▶ goroutine k ──▶
```

The node's output channel is closed once all input goroutines have exited.

---

## Batching

`runBatch` accumulates items in a `[]any` slice and flushes when either the
size limit is reached or a timeout fires.

The timer is **off** when the batch is empty, started when the first item
arrives, and reset after every flush. This ensures a partial batch always
drains within `timeout` of its first item, regardless of upstream throughput.

The critical flush is on **channel close**: when `inCh` closes (`ok == false`),
`flush()` is called unconditionally before returning. This is what makes
graceful drain work for batch stages — once the upstream source stops and its
channel closes, the partial batch is emitted rather than discarded.

---

## Supervision

```
internal/engine/supervise.go
```

`supervise` is a zero-cost abstraction when the policy is inactive
(`MaxRestarts == 0 && OnPanic == PanicPropagate`): it calls the stage
function directly and returns.

When active, it wraps each execution in `runProtected`:

```
for attempt := 0; attempt <= MaxRestarts; attempt++ {
    err := runProtected(ctx, inner, policy.OnPanic)
    if err == nil { return nil }
    if window elapsed { reset attempt counter }
    notify SupervisionHook
    sleep Backoff(attempt)
}
return errBudgetExhausted
```

`runProtected` uses a `defer recover()` to catch panics. The `OnPanic` field
controls what happens: re-panic, convert to a restartable error, or silently
skip the item.

---

## Graceful drain

```
internal/engine/run.go: runWithDrain
```

When `WithDrain(timeout)` is set, `Run` uses a two-phase shutdown:

```
parentCtx  ──cancelled──▶  monitor goroutine
                               │
                          Phase 1: signalDone()
                               │  (sources check done and stop)
                               │
                          Phase 2: wait up to timeout
                          ┌────┴──────────────────────────┐
                          │ pipeline drains naturally      │ timeout fires
                          │ drainCtx.Done() fires          │ drainCancel()
                          └───────────────────────────────┘
```

The key insight is that stages run on `drainCtx` — an **independent** context
derived from `context.Background()`, not from `parentCtx`. Cancelling
`parentCtx` therefore does not directly stop any stage. Only when:

- the drain timeout fires (`drainCancel()` is called), or
- a stage returns an error (the errgroup cancels `egCtx`)

does `drainCtx` cancel and force the remaining stages to exit.

The monitor goroutine lives outside the errgroup. This is important: if it
were inside, a normal pipeline completion (all goroutines return nil) would
leave the monitor blocking on `<-parentCtx.Done()` forever, preventing
`eg.Wait()` from returning. With `defer drainCancel()` in `runWithDrain`,
the context is cancelled after `eg.Wait()` returns, which unblocks the
monitor goroutine and lets it exit cleanly.

---

## Zip

`runZip` reads from two input channels **sequentially**, not concurrently:

```go
// pseudo-code
for {
    a ← inCh1   // blocks until item available or channel closed
    b ← inCh2   // same
    send Pair{a, b}
}
```

The sequential read means: if `inCh1` has items but `inCh2` does not, `runZip`
delivers `a` to the local variable and then blocks waiting for `b`. Meanwhile
`inCh1` continues filling. Size its buffer (`Buffer` option) relative to the
expected rate difference between the two branches.

Both input channels must be part of the same graph. The kitsune layer enforces
this via a pointer-equality check on `a.g != b.g`.

---

## Type erasure

All engine values are `any`. The generic kitsune layer bridges in and out:

```
user fn:  func(context.Context, MyType) (Result, error)
              ↓ wrapped at operator call site
engine fn: func(context.Context, any) (any, error) {
               return userFn(ctx, in.(MyType))
           }
```

On the output side, a Collect terminal unwraps:

```
for item := range outCh {
    results = append(results, item.(T))
}
```

This keeps the entire engine free of type parameters. The `Fn any` field on
`Node` holds whichever of the half-dozen concrete signatures the dispatcher
will cast it to at run time. No reflection is used; every cast is to a
concrete function type.
