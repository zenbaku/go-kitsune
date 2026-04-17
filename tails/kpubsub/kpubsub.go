// Package kpubsub provides Google Cloud Pub/Sub source and sink helpers for
// kitsune pipelines.
//
// The caller owns the [pubsub.Client], subscriptions, and topics: configure
// project IDs, credentials, and subscription settings yourself. Kitsune will
// never create or close them.
//
// Minimal subscribe source:
//
//	client, _ := pubsub.NewClient(ctx, projectID)
//	defer client.Close()
//
//	sub := client.Subscription("my-sub")
//	pipe := kpubsub.Subscribe(sub, func(m *pubsub.Message) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(m.Data, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Pub/Sub uses a callback-based receive model. This package bridges it to
// kitsune's Generate pattern via an internal channel:
//  1. sub.Receive is started in a background goroutine with a derived context.
//  2. The callback sends decoded values into the channel.
//  3. The Generate loop reads from the channel and yields.
//  4. When the pipeline stops (yield returns false) the derived context is
//     cancelled, which causes sub.Receive to return and the goroutine to exit.
//
// Delivery semantics: at-least-once. Messages are Acked after a successful
// yield; on unmarshal failure the message is Nacked and will redeliver. Publish
// calls topic.Publish synchronously (waits for server confirmation via Get
// before returning), making the sink synchronous per item.
package kpubsub

import (
	"context"

	kitsune "github.com/zenbaku/go-kitsune"
	"cloud.google.com/go/pubsub"
)

// Subscribe creates a Pipeline that receives messages from a Pub/Sub
// subscription. Each message is decoded with unmarshal; a decode error causes
// the message to be Nacked and terminates the pipeline.
//
// Messages are Acked after a successful yield. If the pipeline stops before
// the subscription is exhausted (e.g. after Take), the underlying subscription
// receive loop is cancelled cleanly.
//
// The subscription is not closed when the pipeline ends; the caller owns it.
func Subscribe[T any](sub *pubsub.Subscription, unmarshal func(*pubsub.Message) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		type result struct {
			val T
			msg *pubsub.Message
			err error
		}

		ch := make(chan result, 1)
		receiveCtx, cancelReceive := context.WithCancel(ctx)
		defer cancelReceive()

		receiveErr := make(chan error, 1)
		go func() {
			receiveErr <- sub.Receive(receiveCtx, func(_ context.Context, msg *pubsub.Message) {
				v, err := unmarshal(msg)
				if err != nil {
					msg.Nack()
					select {
					case ch <- result{err: err}:
					case <-receiveCtx.Done():
					}
					cancelReceive()
					return
				}
				select {
				case ch <- result{val: v, msg: msg}:
				case <-receiveCtx.Done():
					msg.Nack()
				}
			})
		}()

		for {
			select {
			case <-ctx.Done():
				return nil
			case r := <-ch:
				if r.err != nil {
					return r.err
				}
				if !yield(r.val) {
					r.msg.Ack()
					return nil
				}
				r.msg.Ack()
			case err := <-receiveErr:
				if receiveCtx.Err() != nil {
					return nil // cancelled by us
				}
				return err
			}
		}
	})
}

// Publish returns a sink function that publishes each item to a Pub/Sub topic.
// marshal converts the item into a [*pubsub.Message]. Use with
// [kitsune.Pipeline.ForEach].
//
// The topic is not stopped when the pipeline ends; the caller owns it.
func Publish[T any](topic *pubsub.Topic, marshal func(T) (*pubsub.Message, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		msg, err := marshal(item)
		if err != nil {
			return err
		}
		res := topic.Publish(ctx, msg)
		_, err = res.Get(ctx)
		return err
	}
}
