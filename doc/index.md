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

- :material-valve: **Backpressure**

    Bounded channels between every stage. A slow consumer never causes unbounded queuing: it blocks the upstream instead.

- :material-shield-check-outline: **Type safety**

    `Pipeline[T]` carries its element type through the graph. Every stage transition is checked at compile time.

- :material-lightning-bolt-outline: **Concurrency**

    Add `Concurrency(20)` to any stage to spin up parallel workers. Preserve arrival order with `Ordered()`.

- :material-source-branch: **Error routing**

    Skip, retry with backoff, dead-letter, or circuit-break: configured per stage, composably.

- :material-chart-timeline-variant: **Observability**

    Built-in metrics hook, structured logging via `slog`, and a live inspector dashboard with per-stage sparklines.

- :material-power-plug-outline: **20+ integrations**

    Kafka, Redis, S3, Postgres, NATS, Pub/Sub, SQS, MongoDB, and more: each a separate module via [tails](tails.md).

</div>

---

## Live inspector

![Kitsune Inspector: live pipeline view](screenshoot.png)

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

</div>
