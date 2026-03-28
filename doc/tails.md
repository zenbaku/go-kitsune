# Tails Guide

Tails are optional packages that connect Kitsune pipelines to external systems. Each tail is a separate Go module — you only import and pay for the dependencies you use.

## The user-managed connections principle

Every tail follows the same rule: **you create, configure, and close clients; Kitsune never opens or closes connections.** This gives you full control over connection pooling, TLS, retries, and lifecycle — none of that is hidden inside the library.

```go
// You own the Kafka reader
reader := kafka.NewReader(kafka.ReaderConfig{
    Brokers: []string{"localhost:9092"},
    Topic:   "events",
    GroupID: "my-group",
})
defer reader.Close()

// You pass it to the tail function — Kitsune wraps it in a pipeline
pipe := kkafka.Consume(reader, unmarshal)
```

## Three shapes

Every tail is one of three shapes:

| Shape | What it is | Example |
|---|---|---|
| **Source** | Returns `*Pipeline[T]` | `kkafka.Consume`, `kpostgres.Listen`, `ks3.ListObjects` |
| **Sink** | Returns a `func(ctx, T) error` for use with `ForEach` | `kkafka.Produce`, `kpostgres.Insert`, `ks3.Upload` |
| **Hook** | Implements `kitsune.Hook` for use with `WithHook` | `kotel.New`, `kprometheus.New`, `kdatadog.New` |

Some tails (like `kredis`) provide multiple shapes — a source, a sink, and a `Store`/`Cache` backend.

---

## Messaging

### kkafka — Apache Kafka

```
go get github.com/jonathan/go-kitsune/tails/kkafka
```

**Source** — consume messages from a topic:

```go
reader := kafka.NewReader(kafka.ReaderConfig{
    Brokers: []string{"localhost:9092"},
    Topic:   "events",
    GroupID: "my-group",
})
defer reader.Close()

pipe := kkafka.Consume(reader, func(m kafka.Message) (Event, error) {
    var e Event
    return e, json.Unmarshal(m.Value, &e)
})
pipe.ForEach(handle).Run(ctx)
```

**Sink** — produce messages to a topic:

```go
writer := kafka.NewWriter(kafka.WriterConfig{
    Brokers: []string{"localhost:9092"},
    Topic:   "results",
})
defer writer.Close()

sink := kkafka.Produce(writer, func(r Result) (kafka.Message, error) {
    b, err := json.Marshal(r)
    return kafka.Message{Value: b}, err
})
pipe.ForEach(sink).Run(ctx)
```

See [`examples/` in the kkafka module](../tails/kkafka/) for a complete example.

---

### knats — NATS

```
go get github.com/jonathan/go-kitsune/tails/knats
```

Provides NATS core subscribe/publish sources and sinks, and JetStream consume/publish variants.

---

### kpubsub — Google Cloud Pub/Sub

```
go get github.com/jonathan/go-kitsune/tails/kpubsub
```

Pub/Sub subscribe source and publish sink. You own the `*pubsub.Client`.

---

### ksqs — AWS SQS

```
go get github.com/jonathan/go-kitsune/tails/ksqs
```

Provides a receive source, a send sink, and a batch send sink. You own the `*sqs.Client`.

---

### kkinesis — AWS Kinesis

```
go get github.com/jonathan/go-kitsune/tails/kkinesis
```

Shard consumer source and PutRecords batch sink. You own the `*kinesis.Client`.

---

### kpulsar — Apache Pulsar

```
go get github.com/jonathan/go-kitsune/tails/kpulsar
```

Consumer source and producer sink. You own the `pulsar.Client`.

---

### kmqtt — MQTT

```
go get github.com/jonathan/go-kitsune/tails/kmqtt
```

Subscribe source and publish sink. You own the `mqtt.Client`.

---

### kwebsocket — WebSocket

```
go get github.com/jonathan/go-kitsune/tails/kwebsocket
```

Frame read source and write sink. Uses `nhooyr.io/websocket`. See [`examples/websocket`](../examples/websocket) for a complete in-process server/client example.

---

### kgrpc — gRPC

```
go get github.com/jonathan/go-kitsune/tails/kgrpc
```

Server-streaming source and client-streaming sink. You own the gRPC connection.

---

## Databases

### kpostgres — PostgreSQL

```
go get github.com/jonathan/go-kitsune/tails/kpostgres
```

**Source** — LISTEN/NOTIFY for event-driven pipelines:

```go
conn, _ := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
defer conn.Close(ctx)

pipe := kpostgres.Listen[Event](conn, "events", func(payload string) (Event, error) {
    var e Event
    return e, json.Unmarshal([]byte(payload), &e)
})
pipe.ForEach(handle).Run(ctx)
```

**Sink** — bulk insert via COPY (high throughput):

```go
pool, _ := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
defer pool.Close()

sink := kpostgres.CopyFrom[Row](pool, "my_table",
    []string{"id", "name", "value"},
    func(r Row) []any { return []any{r.ID, r.Name, r.Value} },
)
kitsune.Batch(pipe, 500).ForEach(sink).Run(ctx)
```

**Sink** — single-row insert:

```go
sink := kpostgres.Insert[Row](pool,
    "INSERT INTO my_table (id, name) VALUES ($1, $2)",
    func(r Row) []any { return []any{r.ID, r.Name} },
)
pipe.ForEach(sink).Run(ctx)
```

---

### kredis — Redis

```
go get github.com/jonathan/go-kitsune/tails/kredis
```

`kredis` is the most feature-rich tail — it provides four distinct capabilities:

**Store backend** — distributed state across pipeline runs:

```go
rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
store := kredis.NewStore(rdb, "myapp:")
runner.Run(ctx, kitsune.WithStore(store))
```

**Cache backend** — shared LRU cache for `CacheBy`:

```go
cache := kredis.NewCache(rdb, "myapp:cache:", 5*time.Minute)
runner.Run(ctx, kitsune.WithCache(cache, 5*time.Minute))
```

**DedupSet** — distributed deduplication for long-running pipelines:

```go
// In-memory MemoryDedupSet is fine for bounded batch jobs.
// For long-running pipelines, switch to Redis to bound memory:
dedup := kredis.NewDedupSet(rdb, "myapp:seen:")
pipe.Dedupe(func(e Event) string { return e.ID }, dedup)
```

**Source / sink** — Redis list-based queues:

```go
// List pop source (BLPOP)
pipe := kredis.ListSource[Event](rdb, "myapp:queue", unmarshal)

// List push sink (RPUSH)
sink := kredis.ListSink[Result](rdb, "myapp:results", marshal)
```

See [`examples/redis`](../examples/redis) for a complete example covering all four capabilities.

---

### ksqlite — SQLite

```
go get github.com/jonathan/go-kitsune/tails/ksqlite
```

Query source and insert/batch-insert sinks. You own the `*sql.DB`. See [`examples/sqlite`](../examples/sqlite).

---

### kmongo — MongoDB

```
go get github.com/jonathan/go-kitsune/tails/kmongo
```

Find and Watch sources, InsertMany batch sink. You own the `*mongo.Client`.

---

### kdynamo — AWS DynamoDB

```
go get github.com/jonathan/go-kitsune/tails/kdynamo
```

Scan and Query sources, BatchWriteItem sink. You own the `*dynamodb.Client`.

---

### kclickhouse — ClickHouse

```
go get github.com/jonathan/go-kitsune/tails/kclickhouse
```

Query source and native-protocol batch Insert sink. You own the `driver.Conn`.

---

### kes — Elasticsearch

```
go get github.com/jonathan/go-kitsune/tails/kes
```

Scrolling Search source and Bulk index sink. You own the `*elasticsearch.Client`.

---

## Object storage

### ks3 — S3-compatible object storage

```
go get github.com/jonathan/go-kitsune/tails/ks3
```

Works with AWS S3, Google Cloud Storage (via S3-interop API), MinIO, and any other S3-compatible store.

**Source** — list and parse all objects under a prefix:

```go
cfg, _ := config.LoadDefaultConfig(ctx)
client := s3.NewFromConfig(cfg)

pipe := ks3.ListObjects(client, "my-bucket", "data/2024/", func(key string, body io.Reader) (Event, error) {
    return parseNDJSON(body)
})
pipe.ForEach(handle).Run(ctx)
```

**Source** — stream lines from a single object:

```go
pipe := ks3.Lines(client, "my-bucket", "data/large-file.txt")
pipe.ForEach(processLine).Run(ctx)
```

---

## Files

### kfile — Local files (CSV, JSONL, raw)

```
go get github.com/jonathan/go-kitsune/tails/kfile
```

Zero external dependencies. Sources for CSV rows and JSONL records; sinks for CSV, JSONL, and raw line output. See [`examples/files`](../examples/files).

---

## HTTP

### khttp — HTTP pagination source and webhook sink

```
go get github.com/jonathan/go-kitsune/tails/khttp
```

Paginated GET source that follows cursor-based, offset-based, or link-header pagination. POST/webhook sink. See [`examples/http`](../examples/http).

---

## Observability hooks

Observability tails implement `kitsune.Hook` (and optional extensions like `OverflowHook`, `SupervisionHook`, `BufferHook`). Pass them to `kitsune.WithHook`:

```go
runner.Run(ctx, kitsune.WithHook(hook))
```

### kotel — OpenTelemetry

```
go get github.com/jonathan/go-kitsune/tails/kotel
```

Records per-stage metrics using any OTel-compatible backend. Instruments emitted:

| Metric | Type | Description |
|---|---|---|
| `kitsune.stage.items` | Counter | Items processed; labeled `stage`, `status=ok\|error\|skipped` |
| `kitsune.stage.duration_ms` | Histogram | Processing time per item in milliseconds |
| `kitsune.stage.drops` | Counter | Items dropped by overflow strategies |
| `kitsune.stage.restarts` | Counter | Stage restart events from supervision |
| `kitsune.pipeline.stages` | UpDownCounter | Total number of stages in the pipeline |
| `kitsune.stage.buffer_length` | ObservableGauge | Live channel fill level; labeled `stage`, `capacity` |

```go
meter := otel.Meter("my-app")
hook  := kotel.New(meter)
runner.Run(ctx, kitsune.WithHook(hook))
```

---

### kprometheus — Prometheus

```
go get github.com/jonathan/go-kitsune/tails/kprometheus
```

Counters and histograms per stage, registered against any `prometheus.Registerer`.

| Metric | Type | Description |
|---|---|---|
| `<ns>_stage_items_total` | CounterVec | Items processed; labeled `stage`, `status` |
| `<ns>_stage_duration_seconds` | HistogramVec | Processing time per item |
| `<ns>_stage_drops_total` | CounterVec | Items dropped by overflow |
| `<ns>_stage_restarts_total` | CounterVec | Stage restarts from supervision |

```go
reg  := prometheus.NewRegistry()
hook := kprometheus.New(reg, "myapp") // "myapp" is the metric namespace
runner.Run(ctx, kitsune.WithHook(hook))
```

See [`examples/prometheus`](../examples/prometheus) for a complete example with an HTTP exposition endpoint.

---

### kdatadog — Datadog DogStatsD

```
go get github.com/jonathan/go-kitsune/tails/kdatadog
```

Counts and distributions per stage, sent via DogStatsD. You own the `*statsd.Client`.

---

## Choosing a tail

| Need | Tail |
|---|---|
| High-throughput event streaming | `kkafka` |
| Lightweight pub/sub | `knats` |
| GCP managed messaging | `kpubsub` |
| AWS managed messaging | `ksqs` |
| Database change events | `kpostgres` (LISTEN/NOTIFY) or `kmongo` (Watch) |
| Bulk database writes | `kpostgres` (COPY), `ksqlite`, `kmongo`, `kclickhouse` |
| Distributed state / cache / dedup | `kredis` |
| Object storage ingestion | `ks3` |
| Local file processing | `kfile` |
| Observability (OTel ecosystem) | `kotel` |
| Observability (Prometheus) | `kprometheus` |
| Observability (Datadog) | `kdatadog` |
