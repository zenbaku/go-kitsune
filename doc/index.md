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

- :material-valve: **Automatic backpressure**

    Bounded channels between every stage. A slow consumer blocks its upstream: no unbounded queuing, no dropped items.

- :material-shield-check-outline: **Compile-time type safety**

    `Pipeline[T]` carries its element type through the graph. Every stage transition is checked at compile time via Go generics.

- :material-lightning-bolt-outline: **Per-stage concurrency**

    Add `Concurrency(20)` to any stage to spin up parallel workers. Preserve arrival order with `Ordered()`.

- :material-shield-sync-outline: **Error routing**

    Per-stage `OnError`: `Skip`, `Retry` with exponential backoff, `RetryThen`, `Return`, or `DeadLetter`. Errors are values, not panics.

- :material-chart-timeline-variant: **Observability**

    `MetricsHook`, `LogHook` (structured `slog`), and a [live inspector dashboard](inspector.md). OTel, Prometheus, and Datadog via [tails](tails.md).

- :material-power-plug-outline: **27 integrations**

    Kafka, NATS, RabbitMQ, Postgres, Redis, S3, MongoDB, ClickHouse, SQS, Kinesis, Pub/Sub, and more. Each a separate module via [tails](tails.md).

</div>

[See all features: batching, fan-out, stateful processing, circuit breaker, rate limiting, supervision, and more →](features.md){ .md-button }

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
