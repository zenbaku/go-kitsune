# kkafka batched commit options Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `BatchSize(n)` and `BatchTimeout(d)` `ConsumeOption`s to `kkafka.Consume` so high-throughput consumers can commit Kafka offsets in batches instead of per-message, reducing broker round-trips while keeping existing callers unchanged.

**Architecture:** A `ConsumeOption` functional-options type is added to `tails/kkafka/kkafka.go`. `Consume` gains a variadic `opts ...ConsumeOption` parameter — backward-compatible, existing callers compile with no changes. When neither option is set, the existing per-message commit path runs unchanged. When batching is active, a `pending []kafka.Message` slice accumulates messages; after each successful yield the code checks whether the batch is full or the timeout since the first pending message has elapsed and commits if either is true. On early exit (`yield` returns false) pending messages are not committed, preserving at-least-once semantics identical to the current implementation.

**Tech Stack:** Go generics, `github.com/segmentio/kafka-go`, kitsune's `Generate` primitive.

---

### Task 1: Add ConsumeOption type, consumeConfig, BatchSize, BatchTimeout

**Files:**
- Modify: `tails/kkafka/kkafka.go`

- [ ] **Step 1: Add `"time"` to the import block**

Open `tails/kkafka/kkafka.go`. The current import block is:

```go
import (
	"context"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
)
```

Replace it with:

```go
import (
	"context"
	"time"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
)
```

- [ ] **Step 2: Insert option types before the Consume function**

Directly above the `// Consume creates a Pipeline…` comment, insert:

```go
// ConsumeOption configures the behaviour of [Consume].
type ConsumeOption func(*consumeConfig)

type consumeConfig struct {
	batchSize    int           // 0 or 1 = per-message commits (default)
	batchTimeout time.Duration // 0 = no timer
}

func (c *consumeConfig) batching() bool {
	return c.batchSize > 1 || c.batchTimeout > 0
}

// BatchSize sets how many messages to accumulate before committing offsets to Kafka.
// Default (0) commits each message individually, preserving existing behaviour.
// BatchSize(1) is equivalent to the default.
func BatchSize(n int) ConsumeOption {
	return func(c *consumeConfig) { c.batchSize = n }
}

// BatchTimeout sets the maximum duration to hold uncommitted messages before flushing.
// The clock starts when the first message of the current batch arrives.
// Default (0) disables timer-based flushing.
// BatchTimeout has no effect when no messages are pending.
func BatchTimeout(d time.Duration) ConsumeOption {
	return func(c *consumeConfig) { c.batchTimeout = d }
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd tails/kkafka && go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add tails/kkafka/kkafka.go
git commit -m "feat(kkafka): add ConsumeOption type with BatchSize and BatchTimeout"
```

---

### Task 2: Rewrite Consume to accept options and implement the batching path

**Files:**
- Modify: `tails/kkafka/kkafka.go`

- [ ] **Step 1: Replace the entire Consume function**

The current function starts at `func Consume[T any](reader *kafka.Reader,…`. Replace it (including its godoc) with the following. The non-batching branch is the existing logic verbatim; only the batching branch is new.

```go
// Consume creates a Pipeline that reads messages from a Kafka topic.
// unmarshal converts each [kafka.Message] into a value of type T.
// The reader is not closed when the pipeline ends: the caller owns it.
//
// Delivery semantics: each message is committed individually after it has
// been successfully yielded downstream. If the downstream closes early
// (for example, via [kitsune.Take] or [kitsune.TakeWhile]), the last
// fetched message is not committed. On reconnect the reader will redeliver
// that message. This is intentional at-least-once behaviour.
//
// Use [BatchSize] and [BatchTimeout] to reduce broker round-trips by
// committing offsets in groups:
//
//	pipe := kkafka.Consume(reader, unmarshal,
//	    kkafka.BatchSize(200),
//	    kkafka.BatchTimeout(500*time.Millisecond),
//	)
//
// With batching, more messages may be uncommitted at any moment. Messages
// in the pending batch at the time of an early exit or context cancellation
// are not committed; Kafka redelivers them on reconnect (at-least-once).
func Consume[T any](reader *kafka.Reader, unmarshal func(kafka.Message) (T, error), opts ...ConsumeOption) *kitsune.Pipeline[T] {
	var cfg consumeConfig
	for _, o := range opts {
		o(&cfg)
	}
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		if !cfg.batching() {
			// Original per-message commit path — unchanged.
			for {
				msg, err := reader.FetchMessage(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				v, err := unmarshal(msg)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
				if err := reader.CommitMessages(ctx, msg); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
			}
		}

		// Batching path: accumulate messages, commit in bulk.
		var pending []kafka.Message
		var batchStart time.Time

		commit := func() error {
			if err := reader.CommitMessages(ctx, pending...); err != nil {
				return err
			}
			pending = pending[:0]
			return nil
		}

		for {
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			v, err := unmarshal(msg)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil // at-least-once: pending messages not committed
			}

			if len(pending) == 0 {
				batchStart = time.Now()
			}
			pending = append(pending, msg)

			batchFull := cfg.batchSize > 1 && len(pending) >= cfg.batchSize
			timedOut := cfg.batchTimeout > 0 && time.Since(batchStart) >= cfg.batchTimeout

			if batchFull || timedOut {
				if err := commit(); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
			}
		}
	})
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd tails/kkafka && go build ./...
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add tails/kkafka/kkafka.go
git commit -m "feat(kkafka): implement batched commit path in Consume"
```

---

### Task 3: Write batch commit integration tests

**Files:**
- Modify: `tails/kkafka/kkafka_test.go`

All tests follow the existing pattern: they call `brokerAddr(t)` which skips automatically when `KAFKA_BROKER` is not set.

- [ ] **Step 1: Add `"time"` to the import block in the test file**

The current imports are:

```go
import (
	"context"
	"encoding/json"
	"os"
	"testing"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kkafka"
)
```

Replace with:

```go
import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kkafka"
)
```

- [ ] **Step 2: Add TestConsumeBatchSize**

Append after the existing `TestConsumeProduce` function:

```go
func TestConsumeBatchSize(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-batchsize-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	// Produce 6 messages.
	msgs := make([]Msg, 6)
	for i := range msgs {
		msgs[i] = Msg{ID: i + 1}
	}
	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if err := kitsune.FromSlice(msgs).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// BatchSize(3) on 6 messages: two full commits of 3.
	var got []Msg
	err := kkafka.Consume[Msg](reader, unmarshal, kkafka.BatchSize(3)).
		Take(6).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(got))
	}
	for i, m := range got {
		if m.ID != i+1 {
			t.Errorf("msg[%d]: got ID %d, want %d", i, m.ID, i+1)
		}
	}
}
```

- [ ] **Step 3: Add TestConsumeBatchTimeout**

```go
func TestConsumeBatchTimeout(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-batchtimeout-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if err := kitsune.FromSlice([]Msg{{1}, {2}, {3}}).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// BatchSize(100) would never fill naturally with only 3 messages.
	// BatchTimeout(10ms) flushes any partial batch within 10ms of the first
	// pending message, ensuring the pipeline doesn't stall at teardown.
	var got []Msg
	err := kkafka.Consume[Msg](reader, unmarshal,
		kkafka.BatchSize(100),
		kkafka.BatchTimeout(10*time.Millisecond),
	).
		Take(3).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
}
```

- [ ] **Step 4: Add TestConsumeBatchEarlyExit**

```go
func TestConsumeBatchEarlyExit(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-earlyexit-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	msgs := make([]Msg, 6)
	for i := range msgs {
		msgs[i] = Msg{ID: i + 1}
	}
	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if err := kitsune.FromSlice(msgs).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// Take(4) with BatchSize(3): first batch of 3 is committed; the 4th
	// message is pending when yield returns false. Pipeline must return nil
	// (not an error) and leave the 4th message uncommitted (at-least-once).
	var got []Msg
	err := kkafka.Consume[Msg](reader, unmarshal, kkafka.BatchSize(3)).
		Take(4).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("expected nil on early exit, got: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got))
	}
}
```

- [ ] **Step 5: Run tests — verify they skip cleanly without a broker**

```bash
cd tails/kkafka && go test ./... -v -run 'TestConsumeBatch|TestConsumeBatchTimeout|TestConsumeBatchEarlyExit'
```

Expected output (no broker set):

```
--- SKIP: TestConsumeBatchSize (0.00s)
    kkafka_test.go:17: KAFKA_BROKER not set — skipping integration test
--- SKIP: TestConsumeBatchTimeout (0.00s)
    kkafka_test.go:17: KAFKA_BROKER not set — skipping integration test
--- SKIP: TestConsumeBatchEarlyExit (0.00s)
    kkafka_test.go:17: KAFKA_BROKER not set — skipping integration test
PASS
```

- [ ] **Step 6: Commit**

```bash
git add tails/kkafka/kkafka_test.go
git commit -m "test(kkafka): add BatchSize, BatchTimeout, and early-exit integration tests"
```

---

### Task 4: Update package godoc and doc/tails.md

**Files:**
- Modify: `tails/kkafka/kkafka.go` (package comment at top of file)
- Modify: `doc/tails.md`

- [ ] **Step 1: Replace the package comment in kkafka.go**

The current package comment at the very top of the file (lines 1–18) is:

```go
// Package kkafka provides Kafka source and sink helpers for kitsune pipelines.
//
// Users own the [kafka.Reader] and [kafka.Writer] — configure brokers, topics,
// group IDs, and TLS yourself. Kitsune will never create or close them.
//
// Minimal consumer pipeline:
//
//	reader := kafka.NewReader(kafka.ReaderConfig{
//	    Brokers: []string{"localhost:9092"},
//	    Topic:   "events",
//	    GroupID: "my-group",
//	})
//	defer reader.Close()
//
//	pipe := kkafka.Consume(reader, func(m kafka.Message) (Event, error) {
//	    return json.Unmarshal(m.Value, &Event{})...
//	})
//	pipe.ForEach(handle).Run(ctx)
package kkafka
```

Replace it with:

```go
// Package kkafka provides Kafka source and sink helpers for kitsune pipelines.
//
// Users own the [kafka.Reader] and [kafka.Writer]: configure brokers, topics,
// group IDs, and TLS yourself. Kitsune will never create or close them.
//
// Minimal consumer pipeline:
//
//	reader := kafka.NewReader(kafka.ReaderConfig{
//	    Brokers: []string{"localhost:9092"},
//	    Topic:   "events",
//	    GroupID: "my-group",
//	})
//	defer reader.Close()
//
//	pipe := kkafka.Consume(reader, func(m kafka.Message) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(m.Value, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Batch commits (high-throughput consumers):
//
// By default Consume commits each message individually. Use [BatchSize] and
// [BatchTimeout] to commit in groups and reduce broker round-trips:
//
//	pipe := kkafka.Consume(reader, unmarshal,
//	    kkafka.BatchSize(200),
//	    kkafka.BatchTimeout(500*time.Millisecond),
//	)
//
// Messages are yielded immediately; commits are deferred until BatchSize
// messages have been processed or BatchTimeout elapses since the first message
// in the current batch. Uncommitted messages redeliver on reconnect
// (at-least-once).
package kkafka
```

- [ ] **Step 2: Insert batch commits subsection in doc/tails.md**

In `doc/tails.md`, find the paragraph that ends with:

```
See [`examples/` in the kkafka module](../tails/kkafka/) for a complete example.
```

Insert the following block **between** the "Duplicate handling…" sentence and the "See \[`examples/`..." line (i.e. after line 95, before line 97 in the current file):

```markdown

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

```

- [ ] **Step 3: Build and verify**

```bash
cd tails/kkafka && go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add tails/kkafka/kkafka.go doc/tails.md
git commit -m "docs(kkafka): document BatchSize and BatchTimeout options"
```

---

### Task 5: Mark roadmap item done

**Files:**
- Modify: `doc/roadmap.md`

- [ ] **Step 1: Find and update the roadmap entry**

In `doc/roadmap.md`, find this line (in the Ecosystem section):

```
- [ ] **`kkafka` batched commit variant**: `kkafka.Consume` commits each Kafka message individually after `yield`, which serializes commit latency with processing latency. For high-throughput consumers, batched commits (flush N offsets at once, or on a timer) reduce broker round-trips significantly. Add a `ConsumeWithBatchCommit(reader, batchSize, batchTimeout, unmarshal)` variant that accumulates offsets and commits them at natural batch boundaries.
```

Replace with:

```
- [x] **`kkafka` batched commit variant**: `kkafka.Consume` commits each Kafka message individually after `yield`, which serializes commit latency with processing latency. For high-throughput consumers, batched commits (flush N offsets at once, or on a timer) reduce broker round-trips significantly. Implemented as `BatchSize(n)` and `BatchTimeout(d)` options on `Consume` (variadic, backward-compatible) rather than a standalone function.
```

- [ ] **Step 2: Commit**

```bash
git add doc/roadmap.md
git commit -m "docs(roadmap): mark kkafka batched commit done"
```

---

## Self-review

**Spec coverage:**
- `ConsumeOption` type and `consumeConfig`: Task 1 ✓
- `BatchSize`, `BatchTimeout` constructors: Task 1 ✓
- `Consume` signature change (variadic, backward-compatible): Task 2 ✓
- Non-batching path unchanged: Task 2 ✓
- Batching path with `batchStart` timer: Task 2 ✓
- At-least-once on early exit: Task 2 (implementation) + Task 3 `TestConsumeBatchEarlyExit` ✓
- `TestConsumeBatchSize`: Task 3 ✓
- `TestConsumeBatchTimeout`: Task 3 ✓
- Package godoc "Batch commits" section: Task 4 ✓
- `doc/tails.md` batch commits subsection: Task 4 ✓
- Roadmap item marked done: Task 5 ✓

**Placeholder scan:** No TBD, TODO, "similar to Task N", or vague steps.

**Type consistency:** `ConsumeOption`, `consumeConfig`, `BatchSize`, `BatchTimeout` defined in Task 1 and used consistently in Tasks 2 and 3. `commit()` closure defined and called only within Task 2's batching branch. No naming drift between tasks.
