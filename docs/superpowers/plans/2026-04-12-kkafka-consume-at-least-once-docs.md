# kkafka.Consume At-Least-Once Semantics Documentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document that `kkafka.Consume` implements at-least-once delivery semantics, specifically that the last fetched message before a pipeline boundary (e.g. `Take`) is not committed and will redeliver on reconnect.

**Architecture:** Documentation-only change across three files: the function godoc in `kkafka.go`, the tail reference guide `doc/tails.md`, and the option matrix `doc/api-matrix.md`. No code or test changes needed.

**Tech Stack:** Go godoc conventions, Markdown.

---

## File Map

| File | Change |
|------|--------|
| `tails/kkafka/kkafka.go` | Expand `Consume` godoc to state commit-per-message behaviour and at-least-once redelivery on early exit |
| `doc/tails.md` | Add a semantics paragraph to the kkafka section (mirrors the kamqp section which calls out "manual ack, at-least-once") |
| `doc/api-matrix.md` | Update the Notes column for the Apache Kafka row to mention at-least-once semantics |

---

### Task 1: Update `Consume` godoc in `kkafka.go`

**Files:**
- Modify: `tails/kkafka/kkafka.go:29-31`

Current godoc (lines 29-31):

```go
// Consume creates a Pipeline that reads messages from a Kafka topic.
// unmarshal converts each [kafka.Message] into a value of type T.
// The reader is not closed when the pipeline ends — the caller owns it.
```

- [ ] **Step 1: Edit the godoc**

Replace the three-line godoc block with the expanded version:

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
func Consume[T any](reader *kafka.Reader, unmarshal func(kafka.Message) (T, error)) *kitsune.Pipeline[T] {
```

Note the em-dash in the original "The reader is not closed when the pipeline ends — the caller owns it." must also be changed to a colon per project style. The replacement above does this.

- [ ] **Step 2: Verify the file compiles**

```bash
cd tails/kkafka && go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add tails/kkafka/kkafka.go
git commit -m "docs(kkafka): document at-least-once semantics in Consume godoc"
```

---

### Task 2: Update `doc/tails.md` kkafka section

**Files:**
- Modify: `doc/tails.md:58-91` (the kkafka source/sink section)

Current content (lines 58-91) provides only code examples with no prose about delivery semantics. The kamqp section (lines 111-127) sets the pattern: it includes a **Delivery semantics** note directly after the sink example.

- [ ] **Step 1: Add a semantics paragraph after the sink block**

Insert the following after the closing ` ``` ` of the sink code block (currently line 89) and before the `See [examples/...]` line:

```markdown
**Delivery semantics**: `Consume` commits each message individually after it has been
successfully yielded downstream (at-least-once). If the downstream closes early — for
example because a `Take` or `TakeWhile` boundary is reached — the last fetched message
is not committed. On reconnect the reader redelivers that message. Duplicate handling
in the consumer is required for exactly-once processing.
```

The full kkafka section should look like this after the edit:

```markdown
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

See [`examples/` in the kkafka module](../tails/kkafka/) for a complete example.
```

- [ ] **Step 2: Commit**

```bash
git add doc/tails.md
git commit -m "docs: add delivery semantics note to kkafka section in tails.md"
```

---

### Task 3: Update `doc/api-matrix.md` Notes column for Apache Kafka

**Files:**
- Modify: `doc/api-matrix.md:467`

Current row (line 467):

```
| Apache Kafka | `tails/kkafka` | `Consume` | `Produce` | segmentio/kafka-go |
```

Target: match the style of the RabbitMQ row (line 470) which states semantics explicitly.

- [ ] **Step 1: Update the Notes column**

Replace the row with:

```
| Apache Kafka | `tails/kkafka` | `Consume` | `Produce` | segmentio/kafka-go; at-least-once: last message before a pipeline boundary redelivers on reconnect |
```

- [ ] **Step 2: Commit**

```bash
git add doc/api-matrix.md
git commit -m "docs: note at-least-once semantics for kkafka in api-matrix"
```

---

### Task 4: Mark roadmap item done

**Files:**
- Modify: `doc/roadmap.md:15`

- [ ] **Step 1: Mark the item complete**

Change:

```markdown
- [ ] **`kkafka.Consume` uncommitted last message on early exit**: When `yield(v)` returns false...
```

To:

```markdown
- [x] **`kkafka.Consume` uncommitted last message on early exit**: When `yield(v)` returns false...
```

- [ ] **Step 2: Commit**

```bash
git add doc/roadmap.md
git commit -m "chore: mark kkafka.Consume at-least-once docs done in roadmap"
```

---

## Self-Review

**Spec coverage:**
- Roadmap says: "Add an explicit note to the `Consume` godoc stating that the last message before a pipeline boundary will redeliver on reconnect." — Task 1 covers this exactly.
- Tasks 2 and 3 are additive improvements to bring kkafka documentation to parity with kamqp.
- Task 4 closes the roadmap item.

**Placeholder scan:** No TBDs, no "add appropriate X", all code is complete.

**Type consistency:** No types introduced; all edits are documentation.
