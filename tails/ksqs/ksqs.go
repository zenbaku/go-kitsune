// Package ksqs provides AWS SQS source and sink helpers for kitsune pipelines.
//
// Users own the SQS client — configure credentials, region, and endpoint
// yourself. Kitsune will never create or close clients.
//
// Minimal consumer pipeline:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	client := sqs.NewFromConfig(cfg)
//
//	pipe := ksqs.Receive(client, queueURL, func(m types.Message) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal([]byte(aws.ToString(m.Body)), &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Batch sink (up to 10 messages per request):
//
//	kitsune.Batch(pipe, 10).
//	    ForEach(ksqs.SendBatch(client, queueURL, marshal)).
//	    Run(ctx)
package ksqs

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	kitsune "github.com/zenbaku/go-kitsune"
)

// SQSClient is the subset of the AWS SQS API used by this package.
// *sqs.Client satisfies this interface; it can also be stubbed in tests.
type SQSClient interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	SendMessageBatch(ctx context.Context, params *sqs.SendMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
}

// Receive creates a Pipeline that long-polls an SQS queue and yields decoded
// messages. Messages are deleted from the queue after a successful yield.
// If unmarshal fails the message is not deleted and the pipeline terminates.
//
// The client is not closed when the pipeline ends — the caller owns it.
func Receive[T any](client SQSClient, queueURL string, unmarshal func(types.Message) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		for {
			if ctx.Err() != nil {
				return nil
			}
			out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
				QueueUrl:            aws.String(queueURL),
				MaxNumberOfMessages: 10,
				WaitTimeSeconds:     20,
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}

			for _, msg := range out.Messages {
				v, err := unmarshal(msg)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
				_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
					QueueUrl:      aws.String(queueURL),
					ReceiptHandle: msg.ReceiptHandle,
				})
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
			}
		}
	})
}

// Send returns a sink function that sends each item as a single SQS message.
// marshal converts the item into the message body string. Use with
// [kitsune.Pipeline.ForEach].
//
// The client is not closed when the pipeline ends — the caller owns it.
func Send[T any](client SQSClient, queueURL string, marshal func(T) (string, error)) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		body, err := marshal(item)
		if err != nil {
			return err
		}
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:    aws.String(queueURL),
			MessageBody: aws.String(body),
		})
		return err
	}
}

// SendBatch returns a sink function that sends a batch of items as SQS
// messages. SQS limits batch size to 10; pass items via Batch(p, 10).ForEach.
// marshal converts each item into the message body string. Use with
// [kitsune.Pipeline.ForEach] after [kitsune.Batch].
//
// The client is not closed when the pipeline ends — the caller owns it.
func SendBatch[T any](client SQSClient, queueURL string, marshal func(T) (string, error)) func(context.Context, []T) error {
	return func(ctx context.Context, items []T) error {
		const maxBatch = 10
		for start := 0; start < len(items); start += maxBatch {
			end := start + maxBatch
			if end > len(items) {
				end = len(items)
			}
			chunk := items[start:end]

			entries := make([]types.SendMessageBatchRequestEntry, len(chunk))
			for i, item := range chunk {
				body, err := marshal(item)
				if err != nil {
					return err
				}
				entries[i] = types.SendMessageBatchRequestEntry{
					Id:          aws.String(strconv.Itoa(start + i)),
					MessageBody: aws.String(body),
				}
			}

			out, err := client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
				QueueUrl: aws.String(queueURL),
				Entries:  entries,
			})
			if err != nil {
				return err
			}
			if len(out.Failed) > 0 {
				f := out.Failed[0]
				return fmt.Errorf("ksqs: SendMessageBatch partial failure: id=%s code=%s message=%s",
					aws.ToString(f.Id), aws.ToString(f.Code), aws.ToString(f.Message))
			}
		}
		return nil
	}
}
