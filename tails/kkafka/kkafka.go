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

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
)

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
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil // context cancelled — clean exit
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
