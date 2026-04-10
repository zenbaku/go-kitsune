# Kitsune Documentation

| Document | Audience | Description |
|---|---|---|
| [Getting Started](getting-started.md) | New users | Mental model, first pipeline, concurrency, errors, testing |
| [Operator Catalog](operators.md) | All users | Every source, transform, terminal, and option with signature and description |
| [Inspector Dashboard](inspector.md) | All users | Live web dashboard: pipeline DAG, per-stage metrics, sparklines, stop/restart, dark/light theme |
| [Tuning Guide](tuning.md) | All users | Buffer sizing, concurrency, batching, memory trade-offs |
| [Internals](internals.md) | Contributors / curious | DAG, runtime, concurrency models, supervision, drain |
| [Tails Guide](tails.md) | All users | Connecting to Kafka, Redis, S3, Postgres, and more |
| [Benchmarks](benchmarks.md) | All users | Throughput, backpressure, concurrency scaling, and latency percentiles on Apple M1 |
| [Comparison Guide](comparison.md) | Evaluating | When to use Kitsune vs goroutines, conc, go-streams, RxGo, Watermill, Benthos, Machinery |
