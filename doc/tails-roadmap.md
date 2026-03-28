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
| `tails/kprometheus` | Prometheus Hook — counters and histograms per stage |
| `tails/knats` | NATS core subscribe/publish + JetStream consume/publish |
| `tails/kpubsub` | Google Cloud Pub/Sub subscribe source and publish sink |
| `tails/kgrpc` | gRPC server-streaming source and client-streaming sink |
| `tails/ksqs` | AWS SQS receive source, send sink, batch send sink |
| `tails/kdatadog` | Datadog DogStatsD Hook — counts and distributions per stage |
| `tails/kmongo` | MongoDB Find/Watch sources and InsertMany batch sink |
| `tails/kclickhouse` | ClickHouse Query source and native batch Insert sink |
| `tails/kwebsocket` | WebSocket frame Read source and Write sink (`nhooyr.io/websocket`) |
| `tails/kmqtt` | MQTT Subscribe source and Publish sink (`paho.mqtt.golang`) |
| `tails/kkinesis` | AWS Kinesis shard consumer source and PutRecords batch sink |
| `tails/kpulsar` | Apache Pulsar consumer source and producer sink |
| `tails/kes` | Elasticsearch scrolling Search source and Bulk index sink |
| `tails/kdynamo` | AWS DynamoDB Scan/Query sources and BatchWrite sink |

All 22 tails are shipped. The roadmap is complete.
