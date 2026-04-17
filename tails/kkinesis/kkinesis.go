// Package kkinesis provides AWS Kinesis source and sink helpers for kitsune
// pipelines.
//
// The caller owns the Kinesis client: configure credentials and region
// yourself. Kitsune will never create or close clients.
//
// Consume a shard:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	client := kinesis.NewFromConfig(cfg)
//
//	pipe := kkinesis.Consume(client, "my-stream", "shardId-000000000000",
//	    kinesis.ShardIteratorTypeTrimHorizon, "",
//	    func(r types.Record) (Event, error) {
//	        var e Event
//	        return e, json.Unmarshal(r.Data, &e)
//	    })
//	pipe.ForEach(handle).Run(ctx)
//
// Batch produce:
//
//	kitsune.Batch(events, 500).
//	    ForEach(kkinesis.Produce(client, "my-stream", func(e Event) (types.PutRecordsRequestEntry, error) {
//	        b, err := json.Marshal(e)
//	        return types.PutRecordsRequestEntry{Data: b, PartitionKey: aws.String(e.ID)}, err
//	    })).Run(ctx)
//
// Delivery semantics: Consume is at-most-once from the pipeline's perspective;
// Kinesis does not have consumer-side acks and there is no automatic
// checkpoint. Produce is at-least-once: partial failures from PutRecords
// are returned as errors; combine with [kitsune.Retry] for resilient sinks.
package kkinesis

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/kinesis/types"

	kitsune "github.com/zenbaku/go-kitsune"
)

// KinesisClient is the subset of the AWS Kinesis API used by this package.
// *kinesis.Client satisfies this interface; it can also be stubbed in tests.
type KinesisClient interface {
	GetShardIterator(ctx context.Context, params *kinesis.GetShardIteratorInput, optFns ...func(*kinesis.Options)) (*kinesis.GetShardIteratorOutput, error)
	GetRecords(ctx context.Context, params *kinesis.GetRecordsInput, optFns ...func(*kinesis.Options)) (*kinesis.GetRecordsOutput, error)
	PutRecords(ctx context.Context, params *kinesis.PutRecordsInput, optFns ...func(*kinesis.Options)) (*kinesis.PutRecordsOutput, error)
}

// Consume creates a Pipeline that reads records from a single Kinesis shard.
// iteratorType controls the starting position (e.g. "TRIM_HORIZON", "LATEST",
// "AT_SEQUENCE_NUMBER"). For AT_SEQUENCE_NUMBER or AFTER_SEQUENCE_NUMBER,
// provide startSeq; otherwise pass "".
//
// The source polls GetRecords in a loop. When no records are available
// (MillisBehindLatest == 0 and empty batch), it waits briefly before retrying.
// The client is not closed when the pipeline ends; the caller owns it.
func Consume[T any](client KinesisClient, stream, shardID string, iteratorType types.ShardIteratorType, startSeq string, unmarshal func(types.Record) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		input := &kinesis.GetShardIteratorInput{
			StreamName:        aws.String(stream),
			ShardId:           aws.String(shardID),
			ShardIteratorType: iteratorType,
		}
		if startSeq != "" {
			input.StartingSequenceNumber = aws.String(startSeq)
		}
		iterOut, err := client.GetShardIterator(ctx, input)
		if err != nil {
			return err
		}
		iter := iterOut.ShardIterator

		for iter != nil {
			if ctx.Err() != nil {
				return nil
			}
			out, err := client.GetRecords(ctx, &kinesis.GetRecordsInput{
				ShardIterator: iter,
				Limit:         aws.Int32(1000),
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}

			for _, rec := range out.Records {
				v, err := unmarshal(rec)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}

			iter = out.NextShardIterator

			// If there's nothing left to read, back off briefly.
			if len(out.Records) == 0 && aws.ToInt64(out.MillisBehindLatest) == 0 {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		return nil // shard closed
	})
}

// Produce returns a batch sink function that publishes records to a Kinesis
// stream using PutRecords. Kinesis limits batches to 500 records; use
// [kitsune.Batch] with size ≤ 500.
// marshal converts each item into a PutRecordsRequestEntry (Data + PartitionKey).
// Use with [kitsune.Pipeline.ForEach] after [kitsune.Batch].
//
// The client is not closed when the pipeline ends; the caller owns it.
func Produce[T any](client KinesisClient, stream string, marshal func(T) (types.PutRecordsRequestEntry, error)) func(context.Context, []T) error {
	const maxBatch = 500
	return func(ctx context.Context, items []T) error {
		for start := 0; start < len(items); start += maxBatch {
			end := start + maxBatch
			if end > len(items) {
				end = len(items)
			}
			chunk := items[start:end]

			entries := make([]types.PutRecordsRequestEntry, len(chunk))
			for i, item := range chunk {
				e, err := marshal(item)
				if err != nil {
					return err
				}
				entries[i] = e
			}

			out, err := client.PutRecords(ctx, &kinesis.PutRecordsInput{
				StreamName: aws.String(stream),
				Records:    entries,
			})
			if err != nil {
				return err
			}
			if aws.ToInt32(out.FailedRecordCount) > 0 {
				for _, r := range out.Records {
					if r.ErrorCode != nil {
						return fmt.Errorf("kkinesis: PutRecords partial failure: code=%s message=%s",
							aws.ToString(r.ErrorCode), aws.ToString(r.ErrorMessage))
					}
				}
			}
		}
		return nil
	}
}
