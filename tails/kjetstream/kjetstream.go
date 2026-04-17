// Package kjetstream provides JetStream-specific source and sink helpers for
// kitsune pipelines, covering patterns that tails/knats does not express:
// pull-batch consumers, ordered consumers, key-value watches, and async
// batched publish.
//
// The caller owns all NATS objects; kitsune never creates or closes them. Use
// tails/knats for simple push-style consume ([knats.Consume]) and synchronous
// JetStream publish ([knats.JetStreamPublish]). Reach for kjetstream when you
// need pull fetching, ordered delivery, KV watch streams, or async publish
// throughput.
//
// Pull consumer source:
//
//	cons, _ := js.CreateOrUpdateConsumer(ctx, "STREAM", jetstream.ConsumerConfig{
//	    Name:          "my-consumer",
//	    AckPolicy:     jetstream.AckExplicitPolicy,
//	    DeliverPolicy: jetstream.DeliverAllPolicy,
//	})
//	pipe := kjetstream.Fetch(cons, 100, time.Second, func(msg jetstream.Msg) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(msg.Data(), &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Ordered consumer source (replay a stream from the beginning):
//
//	js, _ := jetstream.New(nc)
//	pipe := kjetstream.OrderedConsume(js, "STREAM",
//	    jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy},
//	    func(msg jetstream.Msg) (Event, error) {
//	        var e Event
//	        return e, json.Unmarshal(msg.Data(), &e)
//	    })
//	pipe.ForEach(handle).Run(ctx)
//
// Key-value watch source (stream all KV changes):
//
//	kv, _ := js.KeyValue(ctx, "BUCKET")
//	pipe := kjetstream.WatchKV(kv, ">", func(e jetstream.KeyValueEntry) (KVEvent, error) {
//	    return KVEvent{Key: e.Key(), Value: e.Value(), Op: e.Operation()}, nil
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Async publish sink (high-throughput publishing with batched acks):
//
//	sink, flush := kjetstream.PublishAsync[Event](js, "EVENTS", 100, func(e Event) ([]byte, error) {
//	    return json.Marshal(e)
//	})
//	if err := pipe.ForEach(sink).Run(ctx); err != nil {
//	    return err
//	}
//	return flush(ctx) // drain any tail ack futures
//
// Delivery semantics: Fetch and FetchBytes use explicit acks (at-least-once);
// each message is acked after a successful yield and nacked on unmarshal
// failure. OrderedConsume uses AckNone (at-most-once; server guarantees
// ordered sequential delivery but does not redeliver). WatchKV is at-most-once
// from the pipeline's perspective. PublishAsync is at-least-once when flush
// is called; always call flush after Run to drain pending ack futures.
package kjetstream

import (
	"context"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/nats-io/nats.go/jetstream"
)

// Fetch creates a Pipeline that pulls messages from a JetStream pull consumer
// in batches of up to batch messages, waiting up to wait for each batch.
// Each message is acked after a successful yield; if unmarshal fails the
// message is nacked and the pipeline terminates.
//
// Pull consumers differ from push consumers ([knats.Consume]) in that the
// client drives flow, making Fetch useful for bursty workloads and
// rate-limited processing. Choose batch to match your processing throughput;
// tune wait based on how sparse your stream is.
//
// The consumer is not closed when the pipeline ends; the caller owns it.
func Fetch[T any](
	cons jetstream.Consumer,
	batch int,
	wait time.Duration,
	unmarshal func(jetstream.Msg) (T, error),
) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			if ctx.Err() != nil {
				return nil
			}
			msgs, err := cons.Fetch(batch, jetstream.FetchMaxWait(wait))
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for msg := range msgs.Messages() {
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
			if err := msgs.Error(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	})
}

// FetchBytes creates a Pipeline that pulls messages from a JetStream pull
// consumer bounded by total payload size. It is identical to [Fetch] but
// sized by bytes rather than count; useful when message sizes vary widely
// and you want to bound memory per batch.
//
// The consumer is not closed when the pipeline ends; the caller owns it.
func FetchBytes[T any](
	cons jetstream.Consumer,
	maxBytes int,
	wait time.Duration,
	unmarshal func(jetstream.Msg) (T, error),
) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			if ctx.Err() != nil {
				return nil
			}
			msgs, err := cons.FetchBytes(maxBytes, jetstream.FetchMaxWait(wait))
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for msg := range msgs.Messages() {
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
			if err := msgs.Error(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	})
}

// OrderedConsume creates a Pipeline that reads from an ephemeral JetStream
// ordered consumer. Ordered consumers provide guaranteed sequential delivery
// with server-side gap detection and automatic consumer recreation; they use
// AckNone policy so no explicit acking is required.
//
// Unlike [knats.Consume] (which takes a pre-existing durable consumer),
// OrderedConsume creates a new ephemeral consumer on each [kitsune.Runner.Run]
// and releases it when the pipeline exits. cfg controls delivery policy,
// filter subjects, and start position.
//
// The JetStream handle is not closed when the pipeline ends; the caller owns it.
func OrderedConsume[T any](
	js jetstream.JetStream,
	stream string,
	cfg jetstream.OrderedConsumerConfig,
	unmarshal func(jetstream.Msg) (T, error),
) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		cons, err := js.OrderedConsumer(ctx, stream, cfg)
		if err != nil {
			return err
		}
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
				// Ordered consumers use AckNone: no nak; just terminate.
				return err
			}
			if !yield(v) {
				return nil
			}
		}
	})
}

// WatchKV creates a Pipeline that watches a JetStream key-value bucket for
// changes. keys is a NATS subject-style pattern: use ">" to watch all keys,
// "prefix.>" for a subtree, or an exact key name. Each entry (PUT, DELETE,
// and PURGE operations) is decoded with unmarshal; filter by
// [jetstream.KeyValueEntry.Operation] inside unmarshal if needed.
//
// The watcher first delivers existing values (the current KV snapshot), then
// sends a nil sentinel to signal init complete, then streams live updates. The
// nil sentinel is skipped automatically; only non-nil entries reach unmarshal.
//
// Additional [jetstream.WatchOpt] values (e.g. [jetstream.IncludeHistory],
// [jetstream.UpdatesOnly], [jetstream.MetaOnly]) can be passed as opts.
//
// The KeyValue handle is not closed when the pipeline ends; the caller owns it.
func WatchKV[T any](
	kv jetstream.KeyValue,
	keys string,
	unmarshal func(jetstream.KeyValueEntry) (T, error),
	opts ...jetstream.WatchOpt,
) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		watcher, err := kv.Watch(ctx, keys, opts...)
		if err != nil {
			return err
		}
		defer watcher.Stop() //nolint:errcheck

		for {
			select {
			case <-ctx.Done():
				return nil
			case entry, ok := <-watcher.Updates():
				if !ok {
					return nil
				}
				if entry == nil {
					// Init done sentinel: existing keys have been delivered.
					continue
				}
				v, err := unmarshal(entry)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}
		}
	})
}

// PublishAsync returns a high-throughput JetStream sink and a flush function.
// The sink uses [jetstream.JetStream.PublishAsync] to pipeline acks; every
// flushEvery items it drains all pending ack futures. Call flush after
// [kitsune.Runner.Run] returns to drain any remaining tail futures.
//
// Use this instead of [knats.JetStreamPublish] when throughput matters: async
// publish pipelines many messages per round-trip whereas sync publish awaits
// one ack per message.
//
// The flush function blocks until all outstanding acks are received or ctx is
// cancelled. Always call flush, even on error, to avoid memory leaks in the
// NATS client's internal ack tracker.
//
// The JetStream handle is not closed when the pipeline ends; the caller owns it.
func PublishAsync[T any](
	js jetstream.JetStream,
	subject string,
	flushEvery int,
	marshal func(T) ([]byte, error),
) (sink func(context.Context, T) error, flush func(context.Context) error) {
	count := 0
	flush = func(ctx context.Context) error {
		select {
		case <-js.PublishAsyncComplete():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	sink = func(ctx context.Context, item T) error {
		data, err := marshal(item)
		if err != nil {
			return err
		}
		if _, err := js.PublishAsync(subject, data); err != nil {
			return err
		}
		count++
		if count >= flushEvery {
			count = 0
			return flush(ctx)
		}
		return nil
	}
	return sink, flush
}

// PutKV returns a sink function that upserts each item into a JetStream
// key-value bucket. key derives the bucket key from the item; marshal
// serialises the value. Use with [kitsune.Pipeline.ForEach].
//
// The KeyValue handle is not closed when the pipeline ends; the caller owns it.
func PutKV[T any](
	kv jetstream.KeyValue,
	key func(T) string,
	marshal func(T) ([]byte, error),
) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		data, err := marshal(item)
		if err != nil {
			return err
		}
		_, err = kv.Put(ctx, key(item), data)
		return err
	}
}
