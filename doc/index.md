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
_, err   := batched.ForEach(bulkInsert, kitsune.Concurrency(4)).Run(ctx)
```

```
go get github.com/zenbaku/go-kitsune
```

---

## What Kitsune handles for you

<div class="grid cards" markdown>

- :material-valve: **[Automatic backpressure](features.md#automatic-backpressure)**

    Bounded channels between stages. A slow consumer blocks its upstream; no unbounded queuing, no dropped items.

- :material-shield-check-outline: **[Compile-time type safety](features.md#compile-time-type-safety)**

    `Pipeline[T]` carries the element type through the graph. Every stage transition is checked at compile time.

- :material-lightning-bolt-outline: **[Per-stage concurrency](features.md#per-stage-concurrency)**

    `Concurrency(20)` spins up parallel workers per stage. `Ordered()` preserves arrival order.

- :material-shield-sync-outline: **[Error routing](features.md#error-routing)**

    Per-stage `OnError`: `Skip`, `Retry` with backoff, `RetryThen`, `Return`. Errors are values, not panics.

- :material-chart-timeline-variant: **[Observability](features.md#observability)**

    `MetricsHook`, `LogHook` (structured `slog`), and a [live inspector dashboard](inspector.md). [`RunSummary`](features.md#run-summary) returns structured results from every run.

- :material-puzzle-outline: **[Composable segments](features.md#stage-composition)**

    `Segment` groups operators into named, graph-visible business units. Compose with `Then` and `Pipeline.Through`.

- :material-play-circle-outline: **[Side-effect modeling](features.md#side-effects)**

    `Effect[I,R]` represents external calls with retry, per-attempt timeout, and required-vs-best-effort outcomes.

- :material-floppy-variant: **[Dev iteration](features.md#higher-level-authoring)**

    `DevStore` snapshots each segment's output. Subsequent runs replay from cache so you can iterate on one stage without rerunning the rest.

- :material-power-plug-outline: **[27 integrations](features.md#27-integrations)**

    Kafka, NATS, RabbitMQ, Postgres, Redis, S3, MongoDB, ClickHouse, SQS, Kinesis, Pub/Sub, and more. Each a separate module via [tails](tails.md).

</div>

[All features →](features.md){ .md-button }

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

- :material-transit-connection-variant: [**Concurrency Guide**](concurrency-guide.md)

    When to reach for `Concurrency(n)`, `Ordered()`, `MapWithKey`, or `Partition`: decision flowchart and worked examples.

- :material-play-circle-outline: [**Examples**](examples.md)

    20 runnable examples with full source code and Go Playground links.

</div>
