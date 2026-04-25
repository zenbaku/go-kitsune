# Per-item context propagation

Kitsune provides two mechanisms for propagating per-item trace context (or any
`context.Context` values) through a pipeline: `ContextCarrier` and
`WithContextMapper`. They solve the same problem with different trade-offs.

---

## Comparison

| | `ContextCarrier` | `WithContextMapper` |
|---|---|---|
| Requires modifying item type | Yes: must implement `Context() context.Context` | No |
| Works with third-party types | No (Kafka messages, protobuf structs, stdlib types cannot be retrofitted) | Yes |
| Granularity | Per-type: any item of that type carries context | Per-stage: configure on individual `Map`, `FlatMap`, `ForEach` stages |
| Opt-in/opt-out | Always active for items that implement the interface | Explicit: must configure each stage |
| Precedence | Lower | Higher: overrides `ContextCarrier` if both apply |

**Default rule:** use `ContextCarrier` when you own the item type. Use `WithContextMapper` when
you cannot modify the item type or when you want per-stage control.

---

## ContextCarrier

`ContextCarrier` is an interface implemented by item types that carry a `context.Context`:

```go
type ContextCarrier interface {
    Context() context.Context
}
```

When Kitsune processes an item that implements `ContextCarrier`, it merges the item's
context into the stage context. Cancellation and deadlines always come from the stage
context: the item context contributes values only (e.g. an active trace span).

### When to use

- You own the item type and can add a `Context()` method.
- Every instance of the type should carry a trace context: no per-stage configuration needed.
- You want stage functions to call `tracer.Start(ctx, "work")` without any extra plumbing.

### Example

```go
// Order carries a per-request trace span set by the HTTP handler.
type Order struct {
    ID     string
    Amount int
    ctx    context.Context
}

func (o Order) Context() context.Context { return o.ctx }

// In a stage function, ctx already contains the span from the item.
processed := kitsune.Map(orders, func(ctx context.Context, o Order) (Invoice, error) {
    _, span := tracer.Start(ctx, "process-order")
    defer span.End()
    // ... work
    return invoice, nil
})
```

Use `kotel.NewWithTracing` to record stage-level spans that appear as parents of
the per-item spans:

```go
hook := kotel.NewWithTracing(otel.Meter("my-app"), otel.Tracer("my-app"))
_, _ = runner.Run(ctx, kitsune.WithHook(hook))
```

---

## WithContextMapper

`WithContextMapper[T]` is a `StageOption` that extracts a context from each item
using a function, with no interface requirement on the item type:

```go
func WithContextMapper[T any](fn func(T) context.Context) StageOption
```

The returned context contributes values only; cancellation still comes from the stage context.

### When to use

- The item type is third-party (Kafka messages, protobuf-generated structs, stdlib types).
- You want per-stage control: some stages use context propagation, others do not.
- You need to extract context from a specific field or header (e.g. a W3C `traceparent` header in a Kafka message).

`WithContextMapper` is supported on `Map`, `FlatMap`, and `ForEach`.

### Example: Kafka messages with OpenTelemetry

```go
// kafkaHeaderCarrier adapts Kafka headers to the OTel TextMapCarrier interface.
type kafkaHeaderCarrier []kafka.Header

func (c kafkaHeaderCarrier) Get(key string) string {
    for _, h := range c {
        if strings.EqualFold(h.Key, key) {
            return string(h.Value)
        }
    }
    return ""
}

func (c kafkaHeaderCarrier) Set(key, val string) {}
func (c kafkaHeaderCarrier) Keys() []string {
    keys := make([]string, len(c))
    for i, h := range c {
        keys[i] = h.Key
    }
    return keys
}

// Extract the trace context from each Kafka message header.
processed := kitsune.Map(messages, processMessage,
    kitsune.WithContextMapper(func(m kafka.Message) context.Context {
        return otel.GetTextMapPropagator().Extract(
            context.Background(),
            kafkaHeaderCarrier(m.Headers),
        )
    }),
)
```

Inside `processMessage`, `ctx` contains the extracted span from the producer:

```go
func processMessage(ctx context.Context, m kafka.Message) (Result, error) {
    _, span := tracer.Start(ctx, "process-message")
    defer span.End()
    // ...
}
```

---

## Precedence

If a stage has `WithContextMapper` set AND the item type implements `ContextCarrier`,
the mapper function takes precedence: `ContextCarrier.Context()` is not called.

This lets you override context extraction on a per-stage basis even for types that
implement the interface.

---

## Mixing both in one pipeline

Items of different types can use different mechanisms in the same pipeline:

```go
// Stage 1: Order implements ContextCarrier: no option needed.
enriched := kitsune.Map(orders, enrich)

// Stage 2: result is a third-party proto type: use WithContextMapper.
sent := kitsune.Map(
    kitsune.Map(enriched, toProto),
    publish,
    kitsune.WithContextMapper(func(p *proto.Order) context.Context {
        return propagator.Extract(context.Background(), &protoCarrier{p})
    }),
)
```
