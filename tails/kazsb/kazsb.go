// Package kazsb provides Azure Service Bus source and sink helpers for
// kitsune pipelines.
//
// Users own the Service Bus clients — configure the connection string or
// credential yourself and pass the receiver/sender to the pipeline functions.
// Kitsune will never create or close clients.
//
// Minimal consumer pipeline (queue or topic subscription):
//
//	client, _ := azservicebus.NewClientFromConnectionString(connStr, nil)
//	receiver, _ := client.NewReceiverForQueue("my-queue", nil)
//	defer receiver.Close(ctx)
//
//	pipe := kazsb.Receive(receiver, func(msg *azservicebus.ReceivedMessage) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal(msg.Body, &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Sink (publish to a queue or topic):
//
//	sender, _ := client.NewSender("my-queue", nil)
//	defer sender.Close(ctx)
//
//	pipe.ForEach(kazsb.Send(sender, func(e Event) (*azservicebus.Message, error) {
//	    b, err := json.Marshal(e)
//	    return &azservicebus.Message{Body: b}, err
//	})).Run(ctx)
package kazsb

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ReceiverClient is the subset of the Azure Service Bus receiver API used by
// this package. *azservicebus.Receiver satisfies this interface; it can also
// be stubbed in tests.
type ReceiverClient interface {
	ReceiveMessages(ctx context.Context, maxMessages int, options *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error)
	CompleteMessage(ctx context.Context, msg *azservicebus.ReceivedMessage, options *azservicebus.CompleteMessageOptions) error
}

// SenderClient is the subset of the Azure Service Bus sender API used by this
// package. *azservicebus.Sender satisfies this interface; it can also be
// stubbed in tests.
type SenderClient interface {
	SendMessage(ctx context.Context, message *azservicebus.Message, options *azservicebus.SendMessageOptions) error
}

// Receive creates a Pipeline that long-polls an Azure Service Bus queue or
// topic subscription and yields decoded messages. Each message is completed
// (acknowledged) after a successful yield. If unmarshal fails the message is
// not completed and the pipeline terminates — the message becomes available
// again after its lock expires.
//
// The receiver is not closed when the pipeline ends — the caller owns it.
func Receive[T any](client ReceiverClient, unmarshal func(*azservicebus.ReceivedMessage) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			if ctx.Err() != nil {
				return nil
			}
			msgs, err := client.ReceiveMessages(ctx, 10, nil)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, msg := range msgs {
				v, err := unmarshal(msg)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
				if err := client.CompleteMessage(ctx, msg, nil); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
			}
		}
	})
}

// Send returns a ForEach-compatible function that publishes each item as a
// single Azure Service Bus message. marshal converts the item into a
// *azservicebus.Message. Use with [kitsune.Pipeline.ForEach].
//
// The sender is not closed when the pipeline ends — the caller owns it.
func Send[T any](client SenderClient, marshal func(T) (*azservicebus.Message, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		msg, err := marshal(item)
		if err != nil {
			return err
		}
		return client.SendMessage(ctx, msg, nil)
	}
}
