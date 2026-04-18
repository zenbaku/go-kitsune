// Package kazeh provides Azure Event Hubs source and sink helpers for
// kitsune pipelines.
//
// The caller owns all Event Hubs clients: configure the connection string or
// credential yourself and pass the partition/producer client to the pipeline
// functions. Kitsune will never create or close clients.
//
// Consume events from a single partition:
//
//	consumerClient, _ := azeventhubs.NewConsumerClientFromConnectionString(connStr, "my-hub", azeventhubs.DefaultConsumerGroup, nil)
//	defer consumerClient.Close(ctx)
//
//	partitionClient, _ := consumerClient.NewPartitionClient("0", nil)
//	defer partitionClient.Close(ctx)
//
//	pipe := kazeh.Consume(partitionClient, func(e *azeventhubs.ReceivedEventData) (Event, error) {
//	    var ev Event
//	    return ev, json.Unmarshal(e.Body, &ev)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Publish events (single event per call):
//
//	producerClient, _ := azeventhubs.NewProducerClientFromConnectionString(connStr, "my-hub", nil)
//	defer producerClient.Close(ctx)
//
//	pipe.ForEach(kazeh.Produce(producerClient, func(e Event) (*azeventhubs.EventData, error) {
//	    b, err := json.Marshal(e)
//	    return &azeventhubs.EventData{Body: b}, err
//	})).Run(ctx)
//
// Publish events in batches (respects 1 MB limit automatically):
//
//	kitsune.Batch(pipe, kitsune.BatchCount(100)).ForEach(kazeh.ProduceBatch(producerClient, marshal)).Run(ctx)
//
// Delivery semantics: at-least-once. Azure Event Hubs does not have
// consumer-side acks; the partition client tracks the offset and redelivers
// from the last checkpoint on reconnect. Use offset checkpointing in your
// consumer configuration to control redelivery scope.
package kazeh

import (
	"context"
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs"

	kitsune "github.com/zenbaku/go-kitsune"
)

// PartitionClient is the subset of the Azure Event Hubs partition client API
// used by this package. *azeventhubs.PartitionClient satisfies this interface;
// it can also be stubbed in tests.
type PartitionClient interface {
	ReceiveEvents(ctx context.Context, count int, options *azeventhubs.ReceiveEventsOptions) ([]*azeventhubs.ReceivedEventData, error)
}

// Consume creates a Pipeline that reads events from a single Azure Event Hubs
// partition and yields decoded values. The pipeline runs until the context is
// cancelled or an error occurs.
//
// For multi-partition consumption, run one Consume pipeline per partition and
// combine them with [kitsune.MergeIndependent].
//
// The partition client is not closed when the pipeline ends; the caller owns it.
func Consume[T any](client PartitionClient, unmarshal func(*azeventhubs.ReceivedEventData) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			if ctx.Err() != nil {
				return nil
			}
			events, err := client.ReceiveEvents(ctx, 100, nil)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, event := range events {
				v, err := unmarshal(event)
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

// Produce returns a ForEach-compatible function that publishes each item as a
// single Azure Event Hubs event. Each item is wrapped in its own batch to
// satisfy the SDK's batch-oriented send API. marshal converts the item into an
// *azeventhubs.EventData. Use with [kitsune.Pipeline.ForEach].
//
// For high-throughput scenarios, prefer [ProduceBatch] with [kitsune.Batch]
// to amortise batch creation overhead across multiple items.
//
// The producer client is not closed when the pipeline ends; the caller owns it.
func Produce[T any](client *azeventhubs.ProducerClient, marshal func(T) (*azeventhubs.EventData, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		event, err := marshal(item)
		if err != nil {
			return err
		}
		batch, err := client.NewEventDataBatch(ctx, nil)
		if err != nil {
			return err
		}
		if err := batch.AddEventData(event, nil); err != nil {
			return err
		}
		return client.SendEventDataBatch(ctx, batch, nil)
	}
}

// ProduceBatch returns a ForEach-compatible function that publishes a slice of
// items as one or more Azure Event Hubs batches, respecting the 1 MB batch
// size limit automatically. Use with [kitsune.Batch] + [kitsune.Pipeline.ForEach].
//
// The producer client is not closed when the pipeline ends; the caller owns it.
func ProduceBatch[T any](client *azeventhubs.ProducerClient, marshal func(T) (*azeventhubs.EventData, error)) func(context.Context, []T) error {
	return func(ctx context.Context, items []T) error {
		batch, err := client.NewEventDataBatch(ctx, nil)
		if err != nil {
			return err
		}
		for _, item := range items {
			event, err := marshal(item)
			if err != nil {
				return err
			}
			err = batch.AddEventData(event, nil)
			if errors.Is(err, azeventhubs.ErrEventDataTooLarge) {
				// Current batch is full: send it and start a new one.
				if err := client.SendEventDataBatch(ctx, batch, nil); err != nil {
					return err
				}
				batch, err = client.NewEventDataBatch(ctx, nil)
				if err != nil {
					return err
				}
				if err := batch.AddEventData(event, nil); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		if batch.NumEvents() > 0 {
			return client.SendEventDataBatch(ctx, batch, nil)
		}
		return nil
	}
}
