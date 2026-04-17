// Package kamqp provides RabbitMQ / AMQP 0-9-1 source and sink helpers for
// kitsune pipelines.
//
// The caller owns all connections and channels: configure brokers, credentials,
// TLS, QoS prefetch, publisher confirms, and exchange/queue topology yourself.
// Kitsune will never create or close connections or channels.
//
// Consume source (manual-ack, at-least-once):
//
//	conn, _ := amqp091.Dial("amqp://guest:guest@localhost:5672/")
//	defer conn.Close()
//	ch, _ := conn.Channel()
//	defer ch.Close()
//	_ = ch.Qos(32, 0, false) // prefetch
//
//	pipe := kamqp.Consume(ch, "events", func(d *amqp091.Delivery) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(d.Body, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Each delivery is acked after a successful downstream yield. If unmarshal
// fails the delivery is nacked (requeue=true by default, see
// [WithRequeueOnNack]) and the pipeline terminates.
//
// Publish sink (to an exchange with a routing key):
//
//	kitsune.FromSlice(events).
//	    ForEach(kamqp.Publish(ch, "events.exchange", "events.created",
//	        func(e Event) (amqp091.Publishing, error) {
//	            b, err := json.Marshal(e)
//	            return amqp091.Publishing{
//	                ContentType: "application/json",
//	                Body:        b,
//	            }, err
//	        })).Run(ctx)
//
// To publish to the default exchange (direct-to-queue), pass exchange="" and
// routingKey=queueName.
//
// Delivery semantics: at-least-once. Each message is acked individually after
// a successful yield; on unmarshal failure or early pipeline exit the delivery
// is nacked and will redeliver. Use [WithAutoAck] to delegate acking to the
// broker (at-most-once for failures after delivery).
package kamqp

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	kitsune "github.com/zenbaku/go-kitsune"
)

// ConsumerClient is the subset of [*amqp.Channel] used by [Consume].
// [*amqp.Channel] satisfies this interface and it can be stubbed in tests.
type ConsumerClient interface {
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
}

// PublisherClient is the subset of [*amqp.Channel] used by [Publish].
// [*amqp.Channel] satisfies this interface and it can be stubbed in tests.
type PublisherClient interface {
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
}

// ConsumeOption configures the behaviour of [Consume].
type ConsumeOption func(*consumeConfig)

type consumeConfig struct {
	consumer      string
	autoAck       bool
	exclusive     bool
	noLocal       bool
	noWait        bool
	args          amqp.Table
	requeueOnNack bool
}

// WithConsumerTag sets the AMQP consumer tag (default: server-generated).
func WithConsumerTag(tag string) ConsumeOption {
	return func(c *consumeConfig) { c.consumer = tag }
}

// WithAutoAck enables broker-side automatic acknowledgement. When set,
// [Consume] will not call Ack or Nack on any delivery.
func WithAutoAck() ConsumeOption {
	return func(c *consumeConfig) { c.autoAck = true }
}

// WithExclusive requests exclusive consumer access to the queue.
func WithExclusive() ConsumeOption {
	return func(c *consumeConfig) { c.exclusive = true }
}

// WithRequeueOnNack controls whether failed deliveries are requeued when
// the pipeline terminates due to an unmarshal error. Default: true.
func WithRequeueOnNack(requeue bool) ConsumeOption {
	return func(c *consumeConfig) { c.requeueOnNack = requeue }
}

// WithConsumeArgs passes additional AMQP arguments to the Consume call.
func WithConsumeArgs(args amqp.Table) ConsumeOption {
	return func(c *consumeConfig) { c.args = args }
}

// Consume creates a [*kitsune.Pipeline] that reads messages from an AMQP
// queue. Each delivery is decoded by unmarshal; on success it is yielded
// downstream and then acknowledged. On unmarshal failure the delivery is
// nacked and the pipeline terminates.
//
// The channel is not closed when the pipeline ends; the caller owns it.
// Call [*amqp.Channel.Qos] on the channel before passing it to control
// prefetch.
func Consume[T any](client ConsumerClient, queue string, unmarshal func(*amqp.Delivery) (T, error), opts ...ConsumeOption) *kitsune.Pipeline[T] {
	cfg := consumeConfig{requeueOnNack: true}
	for _, o := range opts {
		o(&cfg)
	}

	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		deliveries, err := client.Consume(queue, cfg.consumer,
			cfg.autoAck, cfg.exclusive, cfg.noLocal, cfg.noWait, cfg.args)
		if err != nil {
			return err
		}

		for {
			select {
			case <-ctx.Done():
				return nil
			case d, ok := <-deliveries:
				if !ok {
					// Channel closed by broker or by ch.Cancel.
					return nil
				}
				v, err := unmarshal(&d)
				if err != nil {
					if !cfg.autoAck {
						_ = d.Nack(false, cfg.requeueOnNack)
					}
					return err
				}
				if !yield(v) {
					// Downstream stopped: ack the decoded delivery.
					if !cfg.autoAck {
						_ = d.Ack(false)
					}
					return nil
				}
				if !cfg.autoAck {
					if err := d.Ack(false); err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
				}
			}
		}
	})
}

// Publish returns a sink function that publishes each item to an AMQP
// exchange with a fixed routing key. marshal converts the item into an
// [amqp.Publishing] value so callers can set ContentType, Headers,
// DeliveryMode, and other message properties.
//
// To publish to the default exchange (direct-to-queue), pass exchange=""
// and routingKey equal to the queue name.
//
// The channel is not closed when the pipeline ends; the caller owns it.
// Use with [kitsune.Pipeline.ForEach].
func Publish[T any](client PublisherClient, exchange, routingKey string, marshal func(T) (amqp.Publishing, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		msg, err := marshal(item)
		if err != nil {
			return err
		}
		return client.PublishWithContext(ctx, exchange, routingKey, false, false, msg)
	}
}
