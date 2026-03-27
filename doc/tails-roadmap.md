# Kitsune Tails Roadmap

Tails are standalone Go modules under `tails/` that connect pipelines to
external systems. Each tail is an optional dependency — users only pay for
what they import.

## Shipped

| Package | What it provides |
|---|---|
| `tails/kfile` | File/CSV/JSONL sources and sinks (zero deps) |
| `tails/khttp` | Paginated HTTP GET source, POST/webhook sink |
| `tails/kkafka` | Kafka consumer source, producer sink (`segmentio/kafka-go`) |
| `tails/kpostgres` | LISTEN/NOTIFY source, INSERT + COPY batch sink (`pgx/v5`) |
| `tails/kredis` | Redis list/stream source and sink |
| `tails/ks3` | S3-compatible object listing source, line-streaming source (`aws-sdk-go-v2`) |
| `tails/ksqlite` | SQLite query source and exec sink |
| `tails/kotel` | OpenTelemetry Hook for per-stage metrics |

---

## Messaging / event streaming

### `tails/knats` — NATS JetStream
**Dep:** `github.com/nats-io/nats.go`

Source: `Subscribe[T](conn, subject, unmarshal)` — subscribes to a subject,
yields messages.
Sink: `Publish[T](conn, subject, marshal)` — publishes each item.
JetStream variant: `Consume[T](js, stream, consumer, unmarshal)` with
at-least-once delivery and `Ack` on success.

**Why consider it:** NATS is Go-native, has a tiny client, and is common in
microservice stacks as a lighter alternative to Kafka. The client is
well-maintained by the NATS team.

---

### `tails/kpulsar` — Apache Pulsar
**Dep:** `github.com/apache/pulsar-client-go`

Source: `Consume[T](client, topic, subscriptionName, unmarshal)` — reads and
auto-acks messages.
Sink: `Produce[T](client, topic, marshal)` — sends messages.

**Why consider it:** Pulsar is gaining traction for multi-tenant, geo-replicated
event streaming. The client is heavier than NATS (CGO by default); worth
noting in docs.

---

### `tails/kkinesis` — AWS Kinesis
**Dep:** `github.com/aws/aws-sdk-go-v2/service/kinesis`

Source: `Consume[T](client, stream, shardID, unmarshal)` — polls GetRecords,
checkpoints sequence numbers.
Sink: `Produce[T](client, stream, marshal)` — PutRecords in batch.

**Why consider it:** Kinesis is the natural complement to `ks3` for AWS users
doing event-driven pipelines. Shares the same aws-sdk-go-v2 dependency tree.

---

### `tails/ksqs` — AWS SQS
**Dep:** `github.com/aws/aws-sdk-go-v2/service/sqs`

Source: `Receive[T](client, queueURL, unmarshal)` — long-polls, deletes
messages on successful yield.
Sink: `Send[T](client, queueURL, marshal)` — SendMessage.
Batch sink: `SendBatch[T](client, queueURL, marshal)` — SendMessageBatch (up
to 10 messages).

**Why consider it:** SQS is ubiquitous in AWS environments; often used as a
poor-man's Kafka. The aws-sdk-go-v2 dep is shared with `ks3`/`kkinesis`.

---

### `tails/kpubsub` — Google Cloud Pub/Sub
**Dep:** `cloud.google.com/go/pubsub`

Source: `Subscribe[T](client, subscriptionID, unmarshal)` — pulls messages,
acks on success.
Sink: `Publish[T](client, topicID, marshal)` — publishes via the batching
publisher.

**Why consider it:** Natural GCP complement to `ks3` (which supports GCS via
S3 interop). The `cloud.google.com/go/pubsub` client handles batching and
retry internally.

---

### `tails/kwebsocket` — WebSocket
**Dep:** `nhooyr.io/websocket` (or stdlib `golang.org/x/net/websocket`)

Source: `Read[T](conn, unmarshal)` — reads frames, yields parsed values.
Sink: `Write[T](conn, marshal)` — writes one frame per item.

**Why consider it:** Common for real-time market data, telemetry feeds, and
chat/notification systems. Fits the `Generate` pattern naturally.

---

## Databases

### `tails/kmongo` — MongoDB
**Dep:** `go.mongodb.org/mongo-driver/v2`

Source: `Find[T](coll, filter, opts)` — streams cursor results.
Source: `Watch[T](coll, pipeline)` — Change Stream, yields change events.
Batch sink: `InsertMany[T](coll)` — bulk insert.

**Why consider it:** MongoDB is one of the most common Go databases. Change
Streams are a natural fit for event-driven pipelines.

---

### `tails/kes` — Elasticsearch / OpenSearch
**Dep:** `github.com/elastic/go-elasticsearch/v8`

Source: `Search[T](client, index, query, unmarshal)` — scrolling search.
Batch sink: `Bulk[T](client, index, marshal)` — Bulk API for high-throughput
indexing.

**Why consider it:** Elasticsearch is widely used for search and log analytics.
The Bulk API is a natural match for Kitsune's `Batch` operator.

---

### `tails/kclickhouse` — ClickHouse
**Dep:** `github.com/ClickHouse/clickhouse-go/v2`

Source: `Query[T](conn, sql, args, scan)` — streams query rows.
Batch sink: `Insert[T](conn, table, marshal)` — uses the native batch insert
protocol (much faster than individual INSERTs).

**Why consider it:** ClickHouse is the dominant OLAP store in Go stacks.
Its native batch protocol is where the performance gains are, and that maps
perfectly onto `Batch` + a sink.

---

### `tails/kdynamo` — AWS DynamoDB
**Dep:** `github.com/aws/aws-sdk-go-v2/service/dynamodb`

Source: `Scan[T](client, table, unmarshal)` — full-table scan with
pagination.
Source: `Query[T](client, table, keyCondition, unmarshal)` — index-filtered
query.
Batch sink: `BatchWrite[T](client, table, marshal)` — BatchWriteItem (up to
25 items per request).

**Why consider it:** DynamoDB is table stakes for AWS-heavy Go shops. Shares
the aws-sdk-go-v2 dep with `ks3`/`kkinesis`/`ksqs`.

---

## Observability / sinks

### `tails/kprometheus` — Prometheus Hook
**Dep:** `github.com/prometheus/client_golang`

Implements `Hook`, `OverflowHook`, `SupervisionHook` using native Prometheus
counters and histograms, registered against a `prometheus.Registerer`.

**Why consider it:** Many teams use Prometheus directly rather than via OTel.
A native hook avoids the OTel bridge overhead and is simpler to configure.

---

### `tails/kdatadog` — Datadog DogStatsD sink
**Dep:** `github.com/DataDog/datadog-go/v5`

Implements `Hook` using DogStatsD metrics. Adds Datadog-style tags
(`stage`, `pipeline`) to every metric.

**Why consider it:** Datadog is the most common observability platform in
production Go services. A purpose-built hook is simpler than wiring OTel →
Datadog exporter.

---

## Protocols / formats

### `tails/kgrpc` — gRPC server-streaming source
**Dep:** `google.golang.org/grpc`

Source: `Recv[T](stream grpc.ServerStreamingClient[T])` — reads from a
server-streaming RPC until `io.EOF`.
Sink: `Send[T](stream grpc.ClientStreamingClient[T])` — writes items to a
client-streaming RPC.

**Why consider it:** gRPC streaming RPCs are a natural fit for kitsune's
`Generate` pattern; the boilerplate `for { msg, err := stream.Recv() ... }`
is identical to what every tail source does.

---

### `tails/kmqtt` — MQTT
**Dep:** `github.com/eclipse/paho.mqtt.golang`

Source: `Subscribe[T](client, topic, unmarshal)` — subscribes, yields
incoming messages.
Sink: `Publish[T](client, topic, qos, marshal)` — publishes each item.

**Why consider it:** MQTT is the dominant protocol for IoT and embedded
telemetry. Fits pipelines that ingest sensor data and route it to storage or
alerting.

---

## Priority assessment

| Tier | Packages | Rationale |
|---|---|---|
| **High** | `knats`, `kpubsub`, `kprometheus` | High adoption, small deps, fits existing user base |
| **Medium** | `ksqs`, `kmongo`, `kclickhouse`, `kgrpc`, `kdatadog` | Large ecosystems; well-scoped APIs |
| **Lower** | `kkinesis`, `kpulsar`, `kes`, `kdynamo`, `kwebsocket`, `kmqtt` | Valuable but narrower audience or heavier deps |

The Tier 1 packages share a key property: their client libraries are small,
idiomatic Go, and widely used — so the tail itself stays thin and the
dependency cost is easy to justify.
