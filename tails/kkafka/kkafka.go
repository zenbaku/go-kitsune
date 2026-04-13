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

import (
	"context"
	"time"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
)

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

// Produce returns a sink function that writes each item to a Kafka topic.
// marshal converts the item into a [kafka.Message]. The Message's Topic field
// may be left empty when the writer has a topic configured.
// Use with [kitsune.Pipeline.ForEach].
// The writer is not closed when the pipeline ends — the caller owns it.
func Produce[T any](writer *kafka.Writer, marshal func(T) (kafka.Message, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		msg, err := marshal(item)
		if err != nil {
			return err
		}
		return writer.WriteMessages(ctx, msg)
	}
}
