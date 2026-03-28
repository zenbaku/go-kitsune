// Package kdynamo provides AWS DynamoDB source and sink helpers for kitsune
// pipelines.
//
// Users own the DynamoDB client — configure credentials and region yourself.
// Kitsune will never create or close clients.
//
// Full-table scan source:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	client := dynamodb.NewFromConfig(cfg)
//
//	pipe := kdynamo.Scan(client, &dynamodb.ScanInput{TableName: aws.String("events")},
//	    func(item map[string]types.AttributeValue) (Event, error) {
//	        var e Event
//	        return e, attributevalue.UnmarshalMap(item, &e)
//	    })
//	pipe.ForEach(handle).Run(ctx)
//
// Batch write sink (up to 25 items per request):
//
//	kitsune.Batch(pipe, 25).
//	    ForEach(kdynamo.BatchWrite(client, "events", func(e Event) (types.WriteRequest, error) {
//	        item, err := attributevalue.MarshalMap(e)
//	        return types.WriteRequest{PutRequest: &types.PutRequest{Item: item}}, err
//	    })).Run(ctx)
package kdynamo

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	kitsune "github.com/jonathan/go-kitsune"
)

// DynamoClient is the subset of the AWS DynamoDB API used by this package.
// *dynamodb.Client satisfies this interface; it can also be stubbed in tests.
type DynamoClient interface {
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	BatchWriteItem(ctx context.Context, params *dynamodb.BatchWriteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

// Scan creates a Pipeline that performs a paginated full-table scan on a
// DynamoDB table. unmarshal converts each item's attribute map into a value of
// type T.
//
// The client is not closed when the pipeline ends — the caller owns it.
func Scan[T any](client DynamoClient, input *dynamodb.ScanInput, unmarshal func(map[string]types.AttributeValue) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		req := *input // shallow copy; do not mutate the caller's input
		req.ExclusiveStartKey = nil

		for {
			out, err := client.Scan(ctx, &req)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, item := range out.Items {
				v, err := unmarshal(item)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}
			if len(out.LastEvaluatedKey) == 0 {
				return nil
			}
			req.ExclusiveStartKey = out.LastEvaluatedKey
		}
	})
}

// Query creates a Pipeline that performs a paginated DynamoDB query. unmarshal
// converts each item's attribute map into a value of type T.
//
// The client is not closed when the pipeline ends — the caller owns it.
func Query[T any](client DynamoClient, input *dynamodb.QueryInput, unmarshal func(map[string]types.AttributeValue) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		req := *input // shallow copy
		req.ExclusiveStartKey = nil

		for {
			out, err := client.Query(ctx, &req)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			for _, item := range out.Items {
				v, err := unmarshal(item)
				if err != nil {
					return err
				}
				if !yield(v) {
					return nil
				}
			}
			if len(out.LastEvaluatedKey) == 0 {
				return nil
			}
			req.ExclusiveStartKey = out.LastEvaluatedKey
		}
	})
}

// BatchWrite returns a batch sink function that writes items to a DynamoDB
// table using BatchWriteItem. DynamoDB limits batch size to 25; use
// [kitsune.Batch] with size ≤ 25. marshal converts each item into a
// [types.WriteRequest] (either PutRequest or DeleteRequest).
//
// UnprocessedItems are retried once; if they persist the error is returned.
// Use with [kitsune.Pipeline.ForEach] after [kitsune.Batch].
//
// The client is not closed when the pipeline ends — the caller owns it.
func BatchWrite[T any](client DynamoClient, table string, marshal func(T) (types.WriteRequest, error)) func(context.Context, []T) error {
	const maxBatch = 25
	return func(ctx context.Context, items []T) error {
		for start := 0; start < len(items); start += maxBatch {
			end := start + maxBatch
			if end > len(items) {
				end = len(items)
			}
			chunk := items[start:end]

			reqs := make([]types.WriteRequest, len(chunk))
			for i, item := range chunk {
				wr, err := marshal(item)
				if err != nil {
					return err
				}
				reqs[i] = wr
			}

			remaining := reqs
			for len(remaining) > 0 {
				out, err := client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
					RequestItems: map[string][]types.WriteRequest{table: remaining},
				})
				if err != nil {
					return err
				}
				unprocessed := out.UnprocessedItems[table]
				if len(unprocessed) == 0 {
					break
				}
				if len(unprocessed) == len(remaining) {
					return fmt.Errorf("kdynamo: BatchWriteItem: %d items unprocessed (possible throttling)", len(unprocessed))
				}
				remaining = unprocessed
			}
		}
		return nil
	}
}
