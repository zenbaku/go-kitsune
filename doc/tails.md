# Tails Guide

Tails are optional packages that connect Kitsune pipelines to external systems. Each tail is a separate Go module: you only import and pay for the dependencies you use.

## The user-managed connections principle

Every tail follows the same rule: **you create, configure, and close clients; Kitsune never opens or closes connections.** This gives you full control over connection pooling, TLS, retries, and lifecycle, and none of that is hidden inside the library.

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

Some tails (like `kredis`) provide multiple shapes, a source, a sink, and a `Store`/`Cache` backend.

---

## At a glance

<div class="grid cards" markdown>

- :material-message-processing-outline: **[Messaging](#messaging)**: Kafka, NATS, RabbitMQ, Pulsar, MQTT
- :material-cloud-outline: **[Cloud](#cloud)**: Pub/Sub, SQS, Kinesis, DynamoDB
- :material-database-outline: **[Databases](#databases)**: Postgres, MongoDB, ClickHouse, SQLite, Elasticsearch, Redis
- :material-file-document-outline: **[Files & HTTP](#files-http)**: kfile, khttp, S3, WebSocket, gRPC
- :material-chart-line: **[Observability](#observability)**: OpenTelemetry, Prometheus, Datadog

</div>

---

## :material-message-processing-outline: Messaging { #messaging }

### kkafka: Apache Kafka

```
go get github.com/zenbaku/go-kitsune/tails/kkafka
```

**Source**: consume messages from a topic:

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

**Sink**: produce messages to a topic:

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

**Delivery semantics**: `Consume` commits each message individually after it has been
successfully yielded downstream (at-least-once). If the downstream closes early — for
example because a `Take` or `TakeWhile` boundary is reached — the last fetched message
is not committed. On reconnect the reader redelivers that message. Duplicate handling
in the consumer is required for exactly-once processing.

**Batch commits** (high-throughput consumers):

Reduce broker round-trips by committing offsets in groups:

```go
pipe := kkafka.Consume(reader, unmarshal,
    kkafka.BatchSize(200),
    kkafka.BatchTimeout(500*time.Millisecond),
)
```

Messages are still yielded one at a time. `BatchSize` controls how many are
accumulated before a single `CommitMessages` call; `BatchTimeout` flushes any
partial batch after the given duration. Both options can be combined; whichever
fires first triggers the commit. Uncommitted messages at pipeline exit redeliver
on reconnect (at-least-once).

See [`examples/` in the kkafka module](../tails/kkafka/) for a complete example.

---

### knats: NATS

```
go get github.com/zenbaku/go-kitsune/tails/knats
```

Provides NATS core subscribe/publish sources and sinks, and JetStream consume/publish variants.

---

### kamqp: RabbitMQ / AMQP 0-9-1

```
go get github.com/zenbaku/go-kitsune/tails/kamqp
```

**Source**: consume messages from a queue (manual ack, at-least-once):

```go
conn, _ := amqp091.Dial("amqp://guest:guest@localhost:5672/")
defer conn.Close()
ch, _ := conn.Channel()
defer ch.Close()
_ = ch.Qos(32, 0, false) // prefetch

pipe := kamqp.Consume(ch, "events", func(d *amqp091.Delivery) (Event, error) {
    var e Event
    return e, json.Unmarshal(d.Body, &e)
})
pipe.ForEach(handle).Run(ctx)
```

Each delivery is acked after a successful downstream yield. On unmarshal failure the delivery is nacked (requeue=true by default) and the pipeline terminates. Use `kamqp.WithAutoAck()` to delegate acknowledgement to the broker, or `kamqp.WithRequeueOnNack(false)` to dead-letter failed messages.

**Sink**: publish to an exchange with a routing key:

```go
sink := kamqp.Publish(ch, "events.exchange", "events.created",
    func(e Event) (amqp091.Publishing, error) {
        b, err := json.Marshal(e)
        return amqp091.Publishing{
            ContentType:  "application/json",
            DeliveryMode: amqp091.Persistent,
            Body:         b,
        }, err
    })
pipe.ForEach(sink).Run(ctx)
```

To publish directly to a queue (default exchange), pass `exchange=""` and `routingKey=queueName`.

---

### kpulsar: Apache Pulsar

```
go get github.com/zenbaku/go-kitsune/tails/kpulsar
```

Consumer source and producer sink. You own the `pulsar.Client`.

---

### kmqtt: MQTT

```
go get github.com/zenbaku/go-kitsune/tails/kmqtt
```

Subscribe source and publish sink. You own the `mqtt.Client`.

---

## :material-cloud-outline: Cloud { #cloud }

### kpubsub: Google Cloud Pub/Sub

```
go get github.com/zenbaku/go-kitsune/tails/kpubsub
```

Pub/Sub subscribe source and publish sink. You own the `*pubsub.Client`.

---

### ksqs: AWS SQS

```
go get github.com/zenbaku/go-kitsune/tails/ksqs
```

Provides a receive source, a send sink, and a batch send sink. You own the `*sqs.Client`.

---

### kkinesis: AWS Kinesis

```
go get github.com/zenbaku/go-kitsune/tails/kkinesis
```

Shard consumer source and PutRecords batch sink. You own the `*kinesis.Client`.

---

### kdynamo: AWS DynamoDB

```
go get github.com/zenbaku/go-kitsune/tails/kdynamo
```

Scan and Query sources, BatchWriteItem sink. You own the `*dynamodb.Client`.

---

## :material-database-outline: Databases { #databases }

### kpostgres: PostgreSQL

```
go get github.com/zenbaku/go-kitsune/tails/kpostgres
```

**Source**: LISTEN/NOTIFY for event-driven pipelines:

```go
conn, _ := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
defer conn.Close(ctx)

pipe := kpostgres.Listen[Event](conn, "events", func(payload string) (Event, error) {
    var e Event
    return e, json.Unmarshal([]byte(payload), &e)
})
pipe.ForEach(handle).Run(ctx)
```

**Sink**: bulk insert via COPY (high throughput):

```go
pool, _ := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
defer pool.Close()

sink := kpostgres.CopyFrom[Row](pool, "my_table",
    []string{"id", "name", "value"},
    func(r Row) []any { return []any{r.ID, r.Name, r.Value} },
)
kitsune.Batch(pipe, 500).ForEach(sink).Run(ctx)
```

**Sink**: single-row insert:

```go
sink := kpostgres.Insert[Row](pool,
    "INSERT INTO my_table (id, name) VALUES ($1, $2)",
    func(r Row) []any { return []any{r.ID, r.Name} },
)
pipe.ForEach(sink).Run(ctx)
```

---

### kmongo: MongoDB

```
go get github.com/zenbaku/go-kitsune/tails/kmongo
```

Find and Watch sources, InsertMany batch sink. You own the `*mongo.Client`.

---

### kclickhouse: ClickHouse

```
go get github.com/zenbaku/go-kitsune/tails/kclickhouse
```

Query source and native-protocol batch Insert sink. You own the `driver.Conn`.

---

### ksqlite: SQLite

```
go get github.com/zenbaku/go-kitsune/tails/ksqlite
```

Query source and insert/batch-insert sinks. You own the `*sql.DB`. See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete SQLite example.

---

### kes: Elasticsearch

```
go get github.com/zenbaku/go-kitsune/tails/kes
```

Scrolling Search source and Bulk index sink. You own the `*elasticsearch.Client`.

---

### kredis: Redis

```
go get github.com/zenbaku/go-kitsune/tails/kredis
```

`kredis` is the most feature-rich tail: it provides four distinct capabilities:

**Store backend**: distributed state across pipeline runs:

```go
rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
store := kredis.NewStore(rdb, "myapp:")
runner.Run(ctx, kitsune.WithStore(store))
```

**Cache backend**: shared LRU cache for `CacheBy`:

```go
cache := kredis.NewCache(rdb, "myapp:cache:", 5*time.Minute)
runner.Run(ctx, kitsune.WithCache(cache, 5*time.Minute))
```

**DedupSet**: distributed deduplication for long-running pipelines:

`MemoryDedupSet` (the default) accumulates every seen key in a `map[string]struct{}`. For long-running pipelines this grows without bound. `kredis.NewDedupSet` stores keys in a Redis Set, bounding memory to your Redis instance.

```go
// Drop-in replacement: switch MemoryDedupSet → Redis for production.
dedup := kredis.NewDedupSet(rdb, "myapp:seen:")
enriched := kitsune.Dedupe(events, kitsune.WithDedupSet(dedup))
// Or via the Pipeline method form:
//   p.Dedupe(kitsune.WithDedupSet(dedup))
```

The `DedupSet` interface has two methods, `Contains` and `Add`, called in sequence per item. This means two Redis round-trips per item and a theoretical race window (another process could add the same key between the two calls). In practice this is harmless for dedup: a duplicate that slips through is equivalent to the check arriving slightly later. If you need strictly atomic check-and-add, implement `DedupSet` using a Lua script or `SADD`'s return value (which is `0` when the member already exists):

```go
// Atomic single-call implementation using SADD return value.
func (s *redisDedupSet) Contains(ctx context.Context, key string) (bool, error) {
    n, err := s.client.SAdd(ctx, s.redisKey, key).Result()
    return n == 0, err // 0 means already present
}
func (s *redisDedupSet) Add(ctx context.Context, key string) error {
    return nil // already added by Contains
}
```

For probabilistic dedup with bounded memory (useful when approximate correctness is acceptable and the key space is huge), a Bloom filter backend implementing `DedupSet` is a natural extension, and no built-in implementation is provided yet.

**Source / sink**: Redis list-based queues:

```go
// List pop source (BLPOP)
pipe := kredis.ListSource[Event](rdb, "myapp:queue", unmarshal)

// List push sink (RPUSH)
sink := kredis.ListSink[Result](rdb, "myapp:results", marshal)
```

See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete Redis example covering all four capabilities.

---

## :material-file-document-outline: Files & HTTP { #files-http }

### kfile: Local files (CSV, JSONL, raw)

```
go get github.com/zenbaku/go-kitsune/tails/kfile
```

Zero external dependencies. Sources for CSV rows and JSONL records; sinks for CSV, JSONL, and raw line output. See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete file example.

---

### khttp: HTTP pagination source and webhook sink

```
go get github.com/zenbaku/go-kitsune/tails/khttp
```

Paginated GET source that follows cursor-based, offset-based, or link-header pagination. POST/webhook sink. See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete HTTP example.

---

### ks3: S3-compatible object storage

```
go get github.com/zenbaku/go-kitsune/tails/ks3
```

Works with AWS S3, Google Cloud Storage (via S3-interop API), MinIO, and any other S3-compatible store.

**Source**: list and parse all objects under a prefix:

```go
cfg, _ := config.LoadDefaultConfig(ctx)
client := s3.NewFromConfig(cfg)

pipe := ks3.ListObjects(client, "my-bucket", "data/2024/", func(key string, body io.Reader) (Event, error) {
    return parseNDJSON(body)
})
pipe.ForEach(handle).Run(ctx)
```

**Source**: stream lines from a single object:

```go
pipe := ks3.Lines(client, "my-bucket", "data/large-file.txt")
pipe.ForEach(processLine).Run(ctx)
```

---

### kwebsocket: WebSocket

```
go get github.com/zenbaku/go-kitsune/tails/kwebsocket
```

Frame read source and write sink. Uses `nhooyr.io/websocket`. See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete in-process server/client example.

---

### kgrpc: gRPC

```
go get github.com/zenbaku/go-kitsune/tails/kgrpc
```

Server-streaming source and client-streaming sink. You own the gRPC connection.

---

## :material-chart-line: Observability { #observability }

Observability tails implement `kitsune.Hook` (and optional extensions like `OverflowHook`, `SupervisionHook`, `BufferHook`). Pass them to `kitsune.WithHook`:

```go
runner.Run(ctx, kitsune.WithHook(hook))
```

### kotel: OpenTelemetry

```
go get github.com/zenbaku/go-kitsune/tails/kotel
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

### kprometheus: Prometheus

```
go get github.com/zenbaku/go-kitsune/tails/kprometheus
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

See the [examples directory](https://github.com/zenbaku/go-kitsune/tree/main/examples) for a complete example with an HTTP exposition endpoint.

---

### kdatadog: Datadog DogStatsD

```
go get github.com/zenbaku/go-kitsune/tails/kdatadog
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
