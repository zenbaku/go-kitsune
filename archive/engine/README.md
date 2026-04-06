# engine

The `engine` package is the type-erased runtime that powers kitsune pipelines.
It can be understood, tested, and extended independently from the public API.

## Relationship to the kitsune package

```
┌────────────────────────────────────────────────┐
│  kitsune (public)                              │
│  Pipeline[T] · Map · Filter · ForEach · …      │
│  Generic wrappers that translate typed calls   │
│  into engine.Node values                       │
└────────────────────┬───────────────────────────┘
                     │ imports
                     ▼
┌────────────────────────────────────────────────┐
│  engine (this package)                         │
│  Graph · Node · Run · Outbox · Supervise       │
│  All values flow as any; no generics           │
└────────────────────────────────────────────────┘
```

- **engine → kitsune**: never. The engine defines the interfaces (`Store`, `Cache`, `Hook`) that kitsune re-exports as type aliases.
- **kitsune → engine**: always. Every pipeline operator builds `engine.Node` values and calls `engine.Run`.

---

## Execution model

```
 Graph construction (lazy)
 ──────────────────────────────────────────────────────────────
  FromSlice(data)   →  Source node  (ID 0)
  Map(p, fn)        →  Map node     (ID 1, Inputs: [{0,0}])
  .Filter(pred)     →  Filter node  (ID 2, Inputs: [{1,0}])
  .ForEach(fn)      →  Sink node    (ID 3, Inputs: [{2,0}])
                                              ▲
                                      Returns Runner{g}

 Run(ctx) — execution begins here
 ──────────────────────────────────────────────────────────────
  1. Validate(g)          — structural checks
  2. g.InitRefs(store)    — initialize stateful Refs
  3. CreateChannels(g)    — allocate chan any for every output port
  4. CreateOutboxes(g, …) — wrap channels with overflow strategy
  5. Launch one goroutine per node (errgroup)
  6. errgroup.Wait()      — block until all goroutines complete

 Per-node goroutine
 ──────────────────────────────────────────────────────────────
  ┌─────────┐  chan any  ┌─────────┐  chan any  ┌────────┐
  │ Source  │──────────►│  Map    │──────────►│  Sink  │
  │goroutine│           │goroutine│           │gorout. │
  └─────────┘           └─────────┘           └────────┘
       │                      │                    │
    yields items          transforms          terminates
    via Outbox            via Outbox          pipeline on
                                             error or EOF
```

---

## Fan-out patterns

**Partition** — routes each item to one of two outputs based on a predicate:

```
              ┌──────────────┐
              │  Partition   │
  input ─────►│  fn(item)    ├──── port 0 (true)  ──► ...
              │   bool       ├──── port 1 (false) ──► ...
              └──────────────┘
```

**Broadcast** — copies every item to N outputs:

```
              ┌──────────────┐
              │  Broadcast   ├──── port 0 ──► ...
  input ─────►│   N=3        ├──── port 1 ──► ...
              │              ├──── port 2 ──► ...
              └──────────────┘
```

**MapResult** — routes success to port 0, errors to port 1:

```
              ┌──────────────┐
              │  MapResult   ├──── port 0 (ok)    ──► ...
  input ─────►│  fn(item)    ├──── port 1 (error) ──► ...
              │ (T, error)   │
              └──────────────┘
```

---

## Fn signatures by NodeKind

Each `runXxx` function asserts the Fn field to a concrete type.
These are the contracts that kitsune (and any custom API) must honour:

| NodeKind           | Fn signature                                              |
|--------------------|-----------------------------------------------------------|
| `Source`           | `func(ctx, yield func(any) bool) error`                   |
| `Map`              | `func(ctx, item any) (any, error)`                        |
| `FlatMap`          | `func(ctx, item any, yield func(any) error) error`        |
| `Filter`           | `func(item any) bool`                                     |
| `Tap`              | `func(item any) bool` (same gate; always forwards item)   |
| `Sink`             | `func(ctx, item any) error`                               |
| `Partition`        | `func(item any) bool` (true → port 0, false → port 1)    |
| `Reduce`           | `func(acc, item any) any`                                 |
| `Batch`            | no Fn; uses `BatchConvert func([]any) any`                |
| `BroadcastNode`    | no Fn; copies item to all N outboxes                      |
| `Merge`            | no Fn; fans in from multiple input channels               |
| `ZipNode`          | no Fn; uses `ZipConvert func(any, any) any`               |
| `ThrottleNode`     | no Fn; uses `ThrottleDuration`                            |
| `DebounceNode`     | no Fn; uses `ThrottleDuration`                            |
| `ReduceNode`       | `func(acc, item any) any`; `ReduceSeed any`               |
| `MapResultNode`    | `func(ctx, item any) (any, error)` + `MapResultErrWrap`   |
| `WithLatestFromNode` | `func(ctx, primary, latest any) (any, error)`           |
| `TakeWhile`        | `func(item any) bool`                                     |

---

## Backpressure and overflow

Every stage sends via an [Outbox], not directly to a channel.
This enables three overflow strategies without changing stage code:

| Strategy          | Behaviour when buffer full                       |
|-------------------|--------------------------------------------------|
| `OverflowBlock`   | Sender blocks until space is available (default) |
| `OverflowDropNewest` | Incoming item is discarded; no blocking       |
| `OverflowDropOldest` | Oldest buffered item is evicted; no blocking  |

Drop events are reported via `OverflowHook.OnDrop` if the hook implements it.

---

## Supervision

`SupervisionPolicy` on a `Node` enables per-stage restart and panic recovery
without touching the rest of the graph.

```
  stage fn returns error
        │
        ▼
  PanicOnly?  ──yes──► halt immediately (panics still restart)
        │no
        ▼
  restarts < MaxRestarts?  ──no──► return error (halt pipeline)
        │yes
        ▼
  apply Backoff(attempt)
        │
        ▼
  notify SupervisionHook.OnStageRestart
        │
        ▼
  restart stage fn
```

---

## Adding a new NodeKind

1. Add a constant to `NodeKind` in `graph.go`.
2. Add any kind-specific fields to `Node` in `graph.go`.
3. Write a `runXxx` function in `run.go` (follow the pattern of existing runners).
4. Add a `case` to the `switch` in `nodeRunner` in `run.go`.
5. Add a `case` to `kindName` in `run.go`.
6. Write tests in `engine_test.go` covering the happy path, errors, and edge cases.
7. When the feature is ready to expose publicly, add a typed builder in the kitsune package.

---

## Testing the engine directly

Tests in this package construct `Graph` and `Node` values directly, without any
kitsune dependency. This makes it straightforward to test new node kinds or
execution strategies in isolation:

```go
func TestMyNewKind(t *testing.T) {
    g := engine.New()

    src := g.AddNode(&engine.Node{
        Kind:         engine.Source,
        Fn:           sourceFn(1, 2, 3),
        Buffer:       engine.DefaultBuffer,
        ErrorHandler: engine.DefaultHandler{},
    })

    myID := g.AddNode(&engine.Node{
        Kind:         engine.MyNewKind,
        Fn:           myFn,
        Inputs:       []engine.InputRef{{src, 0}},
        Concurrency:  1,
        Buffer:       engine.DefaultBuffer,
        ErrorHandler: engine.DefaultHandler{},
    })

    var out []any
    collectSink(g, myID, &out)

    if err := engine.Run(context.Background(), g, engine.RunConfig{}); err != nil {
        t.Fatal(err)
    }
    // assert out …
}
```
