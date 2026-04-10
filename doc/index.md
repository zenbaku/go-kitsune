---
title: Kitsune
hide:
  - navigation
  - toc
---

<p align="center">
  <img src="go-kitsune-cover.png" alt="Kitsune: type-safe concurrent pipelines for Go" style="max-width: 520px; width: 100%;">
</p>

<p align="center">
  Type-safe, concurrent data pipelines for Go.<br>
  Compose functions into stages: channels, goroutines, backpressure, and error routing are handled for you.
</p>

<p align="center">
  <a href="getting-started/" class="md-button md-button--primary">Get started</a>
  &nbsp;
  <a href="operators/" class="md-button">Operator catalog</a>
</p>

---

```go
raw      := kitsune.From(kafkaStream)
orders   := kitsune.Map(raw, parseOrder)
enriched := kitsune.Map(orders, enrichWithCustomer, kitsune.Concurrency(20))
batched  := kitsune.Batch(enriched, 500, kitsune.BatchTimeout(2*time.Second))
err      := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

```
go get github.com/zenbaku/go-kitsune
```

---

## What Kitsune handles for you

<div class="grid cards" markdown>

- :material-lightning-bolt-outline: **13 M items/sec**

    Linear map throughput on Apple M1. Stage fusion, receive-side micro-batching, and a zero-alloc fast path keep overhead minimal.

- :material-valve: **Automatic backpressure**

    Bounded channels between every stage. A slow consumer blocks its upstream: no unbounded queuing, no dropped items.

- :material-shield-check-outline: **Compile-time type safety**

    `Pipeline[T]` carries its element type through the graph. Every stage transition is checked at compile time via Go generics.

- :material-source-branch: **Fan-out & fan-in**

    `Partition`, `Broadcast`, `Share`, `Balance`, `KeyedBalance`: split streams by predicate, multicast to N consumers, or route by key hash.

- :material-layers-triple-outline: **Batching & windowing**

    `Batch`, `MapBatch`, `Window`, `SlidingWindow`, `SessionWindow`, `ChunkBy`: group items by count, timeout, gap, or key. Composable.

- :material-memory: **Stateful processing**

    `MapWith` / `MapWithKey`: typed `Ref` state scoped to a pipeline run. Key-sharded concurrency gives each entity its own goroutine. The in-process actor model, lock-free by design.

- :material-shield-sync-outline: **Error routing**

    Per-stage `OnError`: `Skip`, `Retry` with exponential backoff, `RetryThen`, `Return`, or `DeadLetter`. Errors are values, not panics.

- :material-electric-switch: **Circuit breaker**

    Configurable failure threshold, cooldown, and half-open probes. Automatically fast-fails downstream items when a dependency is unhealthy.

- :material-speedometer: **Rate limiting**

    Token-bucket `RateLimit` with wait or drop modes. `MapWithKey` enables per-entity rate limiting with zero mutex contention.

- :material-restart: **Supervision & restart**

    `Supervise` wraps any stage with restart-on-error or restart-on-panic semantics, with configurable backoff between attempts.

- :material-puzzle-outline: **Stage composition**

    `Stage[I,O]` is a first-class type. `Then` composes two stages; `Or` adds a typed fallback. Fragments are independently testable.

- :material-chart-timeline-variant: **Observability**

    `Hook` interface, `MetricsHook`, `LogHook` (structured `slog`), and a [live inspector dashboard](inspector.md). OTel, Prometheus, and Datadog via [tails](tails.md).

- :material-clock-fast: **Time-based operators**

    `Ticker`, `Interval`, `Timer`, `Throttle`, `Debounce`, `Sample`, `Timeout`: time sources and per-item deadlines, all context-aware.

- :material-power-plug-outline: **27 integrations**

    Kafka, NATS, RabbitMQ, Postgres, Redis, S3, MongoDB, ClickHouse, SQS, Kinesis, Pub/Sub, and more. Each a separate module via [tails](tails.md).

</div>

---

## Live inspector

![Kitsune Inspector: live pipeline view](screenshot.png)

Add one line to any running pipeline to open a real-time web dashboard with a live DAG, per-stage metrics, and stop/restart controls. [See the inspector guide →](inspector.md)

---

## Where to go next

<div class="grid cards" markdown>

- :material-rocket-launch-outline: [**Getting Started**](getting-started.md)

    Mental model, first pipeline, concurrency, error handling, branching, and testing in ~10 minutes.

- :material-format-list-bulleted: [**Operator Catalog**](operators.md)

    Every source, transform, terminal, and option: with exact signatures and descriptions.

- :material-power-plug-outline: [**Tails**](tails.md)

    Connect pipelines to Kafka, Redis, S3, Postgres, NATS, SQS, MongoDB, and 15+ more systems.

- :material-tune: [**Tuning Guide**](tuning.md)

    Buffer sizing, concurrency settings, batching strategies, and GC-pressure trade-offs.

- :material-play-circle-outline: [**Examples**](examples.md)

    20 runnable examples with full source code and Go Playground links.

</div>
