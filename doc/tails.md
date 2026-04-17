# Tail Interface Contract

This document defines the conventions that all tail packages in `tails/` must follow. It serves as the template for new tails and the audit baseline for existing ones.

---

## Overview

A tail is an adapter that bridges kitsune pipelines to an external system: a message broker, database, HTTP server, object store, file format, or observability backend. Tails live in `tails/<kname>` as independent Go modules and are versioned separately from the core library. You only import the tails you use; each brings its own dependency graph.

---

## Naming

**Primary verbs** for new tails:

- `Consume(client, target, unmarshal, opts...)` returns `*Pipeline[T]` (stream from an external system into the pipeline).
- `Produce(client, target, marshal, opts...)` returns `func(context.Context, T) error` for use with `ForEach` (consume pipeline items and write them out).

**Domain-idiomatic alternatives** accepted when standard in the ecosystem:

| Verb | Equivalent | Used by |
|---|---|---|
| `Subscribe` | Consume | knats (core), kmqtt, kredis pub/sub, kpubsub |
| `Receive` | Consume | kazsb, ksqs |
| `Listen` | Consume | kpostgres (LISTEN/NOTIFY) |
| `Watch` | Consume | kmongo (change streams), kjetstream (KV watch) |
| `Fetch` | Consume | kjetstream (pull consumers) |
| `Read` | Consume | kwebsocket |
| `Find`, `Query`, `Search`, `Scan` | Consume | kmongo, kdynamo, kclickhouse, kes, ksqlite, ks3 |
| `Publish` | Produce | knats, kmqtt, kpubsub, kamqp |
| `Send` | Produce | kazsb, ksqs, kgrpc |
| `Insert`, `InsertMany`, `CopyFrom`, `BatchWrite` | Produce | kpostgres, kmongo, kclickhouse, kdynamo |
| `WriteLines`, `WriteCSV`, `WriteJSON` | Produce | kfile |
| `Upload` | Produce | kgcs |
| `Lines`, `CSV`, `JSON` | Consume | kfile, kgcs, ks3 |
| `Write` | Produce | kwebsocket |
| `GetPages` | Consume | khttp |
| `Post` | Produce | khttp |

**State-backend constructors** (`NewStore`, `NewCache`, `NewDedupSet`) follow the core interface conventions and are not source/sink verbs.

---

## Parameter order

**Read sources:** `(client/conn, target, unmarshal, opts...)`

The "target" is the topic, subject, channel, table, collection, bucket, or URL. When the client already binds a target (e.g. `kafka.Reader` has a configured topic, gRPC stream is already open), omit the target argument.

**Write sinks:** `(client/conn, target, marshal, opts...)`

Same rule: drop target when the client binds it.

---

## Connection lifecycle

Tails never create, open, or close connections, pools, readers, writers, or subscriptions. The caller constructs these objects, passes them in, and is responsible for closing them.

Every tail's package godoc must state this explicitly. The canonical phrasing is:

> The caller owns the [ClientType]: configure [credentials/settings] yourself. Kitsune will never create or close [connections/clients].

Individual function godocs use: "The [conn/client/stream] is not closed when the pipeline ends; the caller owns it."

---

## Error propagation

- Unmarshal/marshal errors terminate the pipeline with that error (fail-fast on malformed data).
- Connection/broker errors terminate the pipeline.
- Errors from user-supplied transform functions are governed by `OnError` on the consuming stage (`Map`, `ForEach`, etc.), not by the tail itself.

---

## Delivery semantics

Every tail must declare in its package godoc one of the following:

**At-least-once:** items may redeliver on reconnect or pipeline restart. This is the default for message brokers that support acks (Kafka after commit, NATS JetStream after ack, SQS after delete, AMQP after ack, Pulsar after ack, Pub/Sub after Ack).

**At-most-once:** items may be lost on pipeline crash (no ack mechanism; e.g. NATS core, MQTT, Redis pub/sub, WebSocket, PostgreSQL LISTEN/NOTIFY).

**Exactly-once-effective:** achievable only with idempotent sinks or external deduplication. Document where this applies (e.g. using `Distinct`/`Dedupe` with `kredis.NewDedupSet`).

For sinks, document whether the sink acks synchronously (ForEach returns only after the item is confirmed by the remote) or asynchronously (e.g. `kjetstream.PublishAsync` batches futures; the caller must call `flush` after `Run`).

Delivery semantics do not apply to:
- Hook tails (`kdatadog`, `kotel`, `kprometheus`): these implement `hooks.Hook` and record metrics; they are not source/sink adapters.
- State backends (`kredis.NewStore`, `NewCache`, `NewDedupSet`): these are not message-passing constructs.

---

## Package godoc template

Every tail's package comment (top of the main `.go` file) must include:

1. One-sentence description.
2. Caller-owns-connection statement.
3. Minimal worked example block (source and sink if both exist).
4. Delivery semantics declaration (or a note that it does not apply).

---

## Tail matrix

| Package | Exported sources / sinks / backends | Delivery semantics |
|---|---|---|
| kamqp | `Consume` (source), `Publish` (sink) | At-least-once (manual ack); `WithAutoAck` makes source at-most-once |
| kazeh | `Consume` (source), `Produce` / `ProduceBatch` (sinks) | At-least-once (offset-based; no consumer ack API) |
| kazsb | `Receive` (source), `Send` (sink) | At-least-once (CompleteMessage after yield; lock expiry redelivers) |
| kclickhouse | `Query` (source), `Insert` (batch sink) | Source: at-most-once. Sink: synchronous batch commit |
| kdatadog | `New` (hook) | N/A: observability hook, not a message broker adapter |
| kdynamo | `Scan`, `Query` (sources), `BatchWrite` (batch sink) | Sources: at-most-once. Sink: at-least-once with retry |
| kes | `Search` (source), `Bulk` (batch sink) | Source: at-most-once (scroll). Sink: synchronous per batch |
| kfile | `Lines`, `CSV`, `JSON` (sources), `WriteLines`, `WriteCSV`, `WriteJSON` (sinks) | N/A: local I/O, no ack mechanism |
| kgcs | `ListObjects`, `Lines` (sources), `Upload` (sink) | Sources: at-most-once. Sink: synchronous object finalisation |
| kgrpc | `Recv` (source), `Send` (sink) | At-most-once (transport layer only; no application ack) |
| khttp | `GetPages` (source), `Post` (sink) | Source: at-most-once. Sink: synchronous; 4xx/5xx terminates pipeline |
| kjetstream | `Fetch`, `FetchBytes`, `OrderedConsume`, `WatchKV` (sources), `PublishAsync`, `PutKV` (sinks) | Fetch/FetchBytes: at-least-once. OrderedConsume/WatchKV: at-most-once. PublishAsync: at-least-once after flush |
| kkafka | `Consume` (source), `Produce` (sink) | At-least-once (per-message or batched commit) |
| kkinesis | `Consume` (source), `Produce` (batch sink) | Source: at-most-once (no ack). Sink: at-least-once (partial failure returned as error) |
| kmongo | `Find`, `Watch` (sources), `InsertMany` (batch sink) | Find: at-most-once. Watch: at-most-once (no auto-resume). Sink: synchronous batch |
| kmqtt | `Subscribe` (source), `Publish` (sink) | At-most-once (QoS 0 may lose; QoS 1/2 broker-level only) |
| knats | `Subscribe` (source, core), `Consume` (source, JetStream), `Publish`, `JetStreamPublish` (sinks) | Subscribe: at-most-once. Consume: at-least-once. JetStreamPublish: synchronous with server ack |
| kotel | `New`, `NewWithTracing` (hooks) | N/A: observability hook |
| kpostgres | `Listen` (source), `Insert` (sink), `CopyFrom` (batch sink) | Listen: at-most-once. Sinks: synchronous; CopyFrom is all-or-nothing per batch |
| kprometheus | `New` (hook) | N/A: observability hook |
| kpubsub | `Subscribe` (source), `Publish` (sink) | At-least-once (Ack after yield; Nack on unmarshal failure) |
| kpulsar | `Consume` (source), `Produce` (sink) | At-least-once (Ack after yield; Nack on unmarshal failure) |
| kredis | `NewStore`, `NewCache`, `NewDedupSet` (state backends), `FromList` (source), `ListPush` (sink) | Backends: N/A. Source: at-most-once (LPOP). Sink: synchronous RPUSH |
| ks3 | `ListObjects`, `Lines` (sources) | At-most-once (read-only; no ack or upload) |
| ksqlite | `Query` (source), `Insert`, `BatchInsert` (sinks) | Source: at-most-once. Sinks: synchronous; each call committed before return |
| ksqs | `Receive` (source), `Send`, `SendBatch` (sinks) | At-least-once (DeleteMessage after yield; visibility timeout redelivers on crash) |
| kwebsocket | `Read` (source), `Write` (sink) | At-most-once (streaming transport; no application ack) |
