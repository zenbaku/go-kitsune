# Choosing a Go Pipeline / Stream Library

This guide compares Kitsune with common alternatives for building data pipelines in Go. The goal is to help you pick the right tool, not to argue that one is universally better.

---

## Quick comparison

| | Kitsune | goroutine+channel | sourcegraph/conc | reugn/go-streams | RxGo | Watermill | Benthos / Redpanda Connect | Machinery |
|---|---|---|---|---|---|---|---|---|
| **Scope** | In-process typed pipeline | Ad-hoc concurrency | Structured concurrency | In-process pipeline | In-process reactive | Distributed messaging | Declarative stream processor | Distributed task queue |
| **Generics** | Yes (Go 1.18+) | N/A | Yes | No (`interface{}`) | No (`interface{}`) | No | N/A (YAML config) | No |
| **Backpressure** | Automatic (bounded channels) | Manual | Manual | Manual | Configurable | Broker-level | Broker-level | Broker-level |
| **DAG topology** | Built-in (Partition, Broadcast, Merge, Zip) | Manual | No | Linear only | Observable chains | Router + handler | YAML processor tree | No |
| **Operator set** | 60+ (Map, FlatMap, Batch, Window, Dedupe, …) | None | ~10 (pool, stream, iter) | ~15 | ReactiveX full set | Router + middleware | 200+ processors | None |
| **Concurrency control** | Per-stage `Concurrency(n)` | Manual | Pool-based | No | Scheduler-based | Per-handler | Per-processor | Worker count |
| **Error handling** | Skip, Retry, RetryThen, DeadLetter, CircuitBreaker | Manual | Panics collected | No | `onError` operator | Retry middleware | Retry processor | Retry per task |
| **Observability** | Inspector dashboard, Hook interface, OTel/Prometheus/Datadog tails | Manual | No | No | No | Middleware | Built-in metrics | Result backend |
| **Distributed** | No | No | No | No | No | Yes | Yes | Yes |
| **Maintained** | Active | N/A | Active | Low activity | Archived | Active | Active (Redpanda) | Low activity |

---

## When to use what

### Hand-rolled goroutines and channels

Use when: your pipeline is 2–3 linear stages, topology is fixed, and you do not want a dependency.

Kitsune adds value when: you need fan-out/fan-in, per-stage concurrency control, error routing, backpressure propagation, or you find yourself reimplementing batching, windowing, retry, or supervision logic. Raw goroutines also give you no structured way to propagate errors from concurrent workers without writing boilerplate.

---

### sourcegraph/conc

Use when: you need structured concurrency primitives: a bounded worker pool, a parallel map over a slice, or safe error collection from a group of goroutines.

`conc` and Kitsune solve different problems. `conc` is "better `errgroup`"; Kitsune is "typed pipeline DAG". They can coexist: use `conc` inside a Kitsune stage function for sub-task parallelism, and Kitsune to compose those stages into a pipeline.

---

### reugn/go-streams

Use when: you want a pipeline library and do not need generics, a large operator set, or built-in observability.

Kitsune differs in: compile-time type safety (generics vs `interface{}`), larger operator catalog, built-in supervision and restart, the inspector dashboard, and an ecosystem of typed tail packages. `go-streams` has not been actively developed since 2022.

---

### RxGo

Not recommended for new projects. The library is archived and no longer maintained. RxGo brought ReactiveX semantics to Go but relied on `interface{}` throughout and never adopted generics.

If migrating from RxGo: Kitsune's operator names differ (`Observable`/`Observer` → `Pipeline`/`ForEach`) but the mental model of composable stream operators translates directly.

---

### Watermill

Use when: your primary concern is routing messages between external brokers (Kafka, RabbitMQ, Google Pub/Sub, NATS, etc.) with at-least-once delivery guarantees and durable subscriptions.

Watermill is a messaging framework; Kitsune is an in-process pipeline library. They complement each other: use Watermill for inter-service message routing and Kitsune for the transformation logic within a service. Kitsune's tails (`kkafka`, `kpubsub`, `knats`, etc.) overlap with Watermill's subscriber adapters: if you only need one direction of a broker connection with in-process transformation, Kitsune's tails are enough; if you need durable subscriptions, consumer groups, or message routing between services, Watermill is the right layer.

---

### Benthos / Redpanda Connect

Use when: you want a standalone stream processor you deploy as a separate binary, configured via YAML, with 200+ connectors and processors already built in.

Benthos is a different category: it is a runtime you deploy, not a library you import. Choose Kitsune when the pipeline logic is part of your Go application, you need compile-time types, custom Go functions, or programmatic control over pipeline structure. Choose Benthos when you want to wire together sources and sinks via configuration without writing application code.

---

### Machinery

Use when: you need a distributed task queue with result backends, scheduled tasks, workflows across multiple workers, and retry policies tracked in an external store (Redis, MongoDB, etc.).

Machinery distributes work across processes and machines. Kitsune processes data within a single process. If you need to distribute work across nodes, use Machinery, Temporal, or Asynq. If you need an in-process pipeline to process data arriving in the current process, use Kitsune.

---

## Summary

Kitsune occupies a specific niche: **in-process, type-safe, operator-rich data pipelines with automatic backpressure**. It is not a distributed system, not a message broker adapter, and not a deployment target. If your pipeline runs inside a single Go process and you want a structured way to compose it with concurrency, error handling, and observability, Kitsune is a good fit. If your problem is distributing work across machines or routing messages between services, look at Watermill, Benthos, or Machinery instead.
