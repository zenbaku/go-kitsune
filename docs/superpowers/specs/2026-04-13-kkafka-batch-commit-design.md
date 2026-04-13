---
title: kkafka batched commit options
date: 2026-04-13
status: approved
---

# kkafka batched commit options

## Problem

`kkafka.Consume` commits each Kafka message individually after `yield`. Under high throughput this serialises commit latency with processing latency and produces one broker round-trip per message. Batching commits (flush N offsets at once, or on a timer) reduces round-trips significantly and is a standard pattern in production Kafka consumers.

## Goal

Add `BatchSize(n int)` and `BatchTimeout(d time.Duration)` options to `kkafka.Consume` so callers can control commit granularity without changing their pipeline topology.

## Non-goals

- No changes to `Produce`.
- No new standalone function (`ConsumeWithBatchCommit` is not added).
- No goroutine-based timer; the timeout check fires at message-loop boundaries only.

---

## API

### Signature change

```go
// Consume creates a Pipeline that reads messages from a Kafka topic.
// opts configures commit behaviour; see BatchSize and BatchTimeout.
func Consume[T any](
    reader    *kafka.Reader,
    unmarshal func(kafka.Message) (T, error),
    opts      ...ConsumeOption,
) *kitsune.Pipeline[T]
```

Existing call sites (`kkafka.Consume(reader, unmarshal)`) compile unchanged.

### New types and constructors

```go
// ConsumeOption configures the behaviour of Consume.
type ConsumeOption func(*consumeConfig)

// BatchSize sets how many messages to accumulate before committing to Kafka.
// Default (0) commits each message individually, preserving the existing behaviour.
// BatchSize(1) is equivalent to the default.
func BatchSize(n int) ConsumeOption

// BatchTimeout sets the maximum duration to hold uncommitted messages before
// flushing. The clock starts when the first message of a new batch arrives.
// Default (0) disables timer-based flushing.
// BatchTimeout has no effect unless at least one message is pending.
func BatchTimeout(d time.Duration) ConsumeOption
```

### Typical usage

```go
pipe := kkafka.Consume(reader, unmarshal,
    kkafka.BatchSize(200),
    kkafka.BatchTimeout(500*time.Millisecond),
)
pipe.ForEach(handle).Run(ctx)
```

---

## Implementation

### consumeConfig

```go
type consumeConfig struct {
    batchSize    int           // 0 or 1 = per-message commits
    batchTimeout time.Duration // 0 = no timer
}

func (c *consumeConfig) batching() bool {
    return c.batchSize > 1 || c.batchTimeout > 0
}
```

### Generate closure (batching path)

```
pending    []kafka.Message   // uncommitted messages
batchStart time.Time        // set when first message enters an empty pending slice

for each fetched message:
    unmarshal → yield
    if yield returns false: return nil  // at-least-once: do not commit pending

    if len(pending) == 0: batchStart = time.Now()
    pending = append(pending, msg)

    batchFull = cfg.batchSize > 1 && len(pending) >= cfg.batchSize
    timedOut  = cfg.batchTimeout > 0 && time.Since(batchStart) >= cfg.batchTimeout

    if batchFull || timedOut:
        reader.CommitMessages(ctx, pending...)
        pending = pending[:0]
```

The non-batching path (default, zero options) is unchanged: commit each message inline after yield.

### At-least-once semantics

Identical to the existing `Consume`. Messages in `pending` at the time of an early exit or crash are not committed; Kafka redelivers them on reconnect. With larger batch sizes, more messages can be in-flight at any moment. This must be documented clearly.

---

## Testing

All tests live in `tails/kkafka/kkafka_test.go`.

| Test | What it verifies |
|---|---|
| existing tests | zero-option Consume is unchanged |
| `TestConsumeBatchSize` | commits fire exactly every n messages (stream of 3n items) |
| `TestConsumeBatchTimeout` | partial batch flushes after timeout elapses before batch is full |
| `TestConsumeBatchBoth` | whichever condition fires first triggers the commit |
| `TestConsumeBatchEarlyExit` | yield=false mid-batch: pipeline returns nil, pending not committed |

Tests follow the existing pattern: they call `brokerAddr(t)` and skip when `KAFKA_BROKER` is not set. Timeout tests use a short duration (10ms) against a real broker.

---

## Documentation

### kkafka.go package doc

Add a "Batch commits" section after the existing minimal consumer example:

```
// Batch commits (high-throughput consumers):
//
//   pipe := kkafka.Consume(reader, unmarshal,
//       kkafka.BatchSize(200),
//       kkafka.BatchTimeout(500*time.Millisecond),
//   )
//
// Messages are yielded immediately; commits are deferred until BatchSize messages
// have been processed or BatchTimeout elapses since the first message in the
// current batch. Uncommitted messages redeliver on reconnect (at-least-once).
```

### doc/tails.md

Add a "Batch commits" subsection under the kkafka heading:

```markdown
**Batch commits** (high-throughput consumers):

Reduce broker round-trips by committing offsets in groups:

​```go
pipe := kkafka.Consume(reader, unmarshal,
    kkafka.BatchSize(200),
    kkafka.BatchTimeout(500*time.Millisecond),
)
​```

Messages are still yielded one at a time. `BatchSize` controls how many are
accumulated before a single `CommitMessages` call; `BatchTimeout` flushes any
partial batch after the given duration. Both options can be combined; whichever
fires first triggers the commit. Uncommitted messages at pipeline exit redeliver
on reconnect (at-least-once).
```

---

## Rollout

- No migration needed: all existing callers are unaffected.
- The `batching()` helper keeps the hot non-batching path free of any branching overhead.
