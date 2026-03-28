// Package knats provides NATS and NATS JetStream source and sink helpers for
// kitsune pipelines.
//
// Users own the [nats.Conn] and JetStream objects — configure servers, TLS,
// credentials, and stream/consumer settings yourself. Kitsune will never
// create or close them.
//
// Core NATS subscribe source:
//
//	nc, _ := nats.Connect(nats.DefaultURL)
//	defer nc.Close()
//
//	pipe := knats.Subscribe(nc, "events", func(msg *nats.Msg) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(msg.Data, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// JetStream consumer source:
//
//	js, _ := jetstream.New(nc)
//	cons, _ := js.Consumer(ctx, "EVENTS", "my-consumer")
//
//	pipe := knats.Consume(cons, func(msg jetstream.Msg) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(msg.Data(), &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
package knats

import (
	"context"
	"errors"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Subscribe creates a Pipeline that reads messages from a NATS subject using
// a synchronous subscription. Each message is decoded with unmarshal; a decode
// error terminates the pipeline with that error.
//
// The connection is not closed when the pipeline ends — the caller owns it.
func Subscribe[T any](conn *nats.Conn, subject string, unmarshal func(*nats.Msg) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		sub, err := conn.SubscribeSync(subject)
		if err != nil {
			return err
		}
		defer sub.Unsubscribe() //nolint:errcheck

		for {
			msg, err := sub.NextMsgWithContext(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
		}
	})
}

// Consume creates a Pipeline that reads messages from a JetStream consumer
// with at-least-once delivery. Each message is acked after a successful yield;
// if unmarshal fails the message is nacked and the pipeline terminates.
//
// The consumer is not closed when the pipeline ends — the caller owns it.
func Consume[T any](cons jetstream.Consumer, unmarshal func(jetstream.Msg) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		msgs, err := cons.Messages()
		if err != nil {
			return err
		}
		defer msgs.Stop()

		for {
			msg, err := msgs.Next()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			v, err := unmarshal(msg)
			if err != nil {
				_ = msg.Nak()
				return err
			}
			if !yield(v) {
				_ = msg.Ack()
				return nil
			}
			if err := msg.Ack(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	})
}

// Publish returns a sink function that publishes each item to a NATS subject.
// marshal converts the item into a byte payload. Use with [kitsune.Pipeline.ForEach].
//
// The connection is not closed when the pipeline ends — the caller owns it.
func Publish[T any](conn *nats.Conn, subject string, marshal func(T) ([]byte, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		data, err := marshal(item)
		if err != nil {
			return err
		}
		return conn.Publish(subject, data)
	}
}

// JetStreamPublish returns a sink function that publishes each item to a
// JetStream subject with acknowledgement. marshal converts the item into a
// byte payload. Use with [kitsune.Pipeline.ForEach].
func JetStreamPublish[T any](js jetstream.JetStream, subject string, marshal func(T) ([]byte, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		data, err := marshal(item)
		if err != nil {
			return err
		}
		_, err = js.Publish(ctx, subject, data)
		return err
	}
}
