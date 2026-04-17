// Package kpulsar provides Apache Pulsar source and sink helpers for kitsune
// pipelines.
//
// The caller owns the [pulsar.Client], consumers, and producers: configure
// brokers, auth, and subscription settings yourself. Kitsune will never create
// or close them.
//
// Note: The Pulsar Go client uses CGO by default. If building without CGO, you
// must set the build tag `pulsar_client_go_no_cgo`.
//
// Consumer source:
//
//	client, _ := pulsar.NewClient(pulsar.ClientOptions{URL: "pulsar://localhost:6650"})
//	defer client.Close()
//
//	consumer, _ := client.Subscribe(pulsar.ConsumerOptions{
//	    Topic:            "my-topic",
//	    SubscriptionName: "my-sub",
//	})
//	defer consumer.Close()
//
//	pipe := kpulsar.Consume(consumer, func(msg pulsar.Message) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(msg.Payload(), &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Producer sink:
//
//	producer, _ := client.CreateProducer(pulsar.ProducerOptions{Topic: "my-topic"})
//	defer producer.Close()
//
//	kitsune.FromSlice(events).
//	    ForEach(kpulsar.Produce(producer, func(e Event) (*pulsar.ProducerMessage, error) {
//	        b, err := json.Marshal(e)
//	        return &pulsar.ProducerMessage{Payload: b}, err
//	    })).Run(ctx)
//
// Delivery semantics: at-least-once. Messages are acked after a successful
// yield; on unmarshal failure the message is nacked and the pipeline
// terminates. Produce sends synchronously per item (waits for broker ack
// via Send before returning).
package kpulsar

import (
	"context"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/apache/pulsar-client-go/pulsar"
)

// Consume creates a Pipeline that reads messages from a Pulsar consumer.
// Messages are acked after a successful yield; if unmarshal fails the message
// is nacked and the pipeline terminates with that error.
//
// The consumer is not closed when the pipeline ends; the caller owns it.
func Consume[T any](consumer pulsar.Consumer, unmarshal func(pulsar.Message) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			msg, err := consumer.Receive(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			v, err := unmarshal(msg)
			if err != nil {
				consumer.Nack(msg)
				return err
			}
			if !yield(v) {
				consumer.Ack(msg)
				return nil
			}
			consumer.Ack(msg)
		}
	})
}

// Produce returns a sink function that sends each item to a Pulsar topic.
// marshal converts the item into a [*pulsar.ProducerMessage]. Use with
// [kitsune.Pipeline.ForEach].
//
// The producer is not closed when the pipeline ends; the caller owns it.
func Produce[T any](producer pulsar.Producer, marshal func(T) (*pulsar.ProducerMessage, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		msg, err := marshal(item)
		if err != nil {
			return err
		}
		_, err = producer.Send(ctx, msg)
		return err
	}
}
