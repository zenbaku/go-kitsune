package kdynamo_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/zenbaku/go-kitsune/tails/kdynamo"
)

// --- in-memory DynamoDB stub ---

type stubDynamo struct {
	items []map[string]types.AttributeValue
}

func (s *stubDynamo) Scan(_ context.Context, params *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	// Simple full-scan: return all items (no pagination in stub).
	return &dynamodb.ScanOutput{Items: s.items, Count: int32(len(s.items))}, nil
}

func (s *stubDynamo) Query(_ context.Context, params *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	// Stub returns all items regardless of condition.
	return &dynamodb.QueryOutput{Items: s.items, Count: int32(len(s.items))}, nil
}

func (s *stubDynamo) BatchWriteItem(_ context.Context, params *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	for _, reqs := range params.RequestItems {
		for _, req := range reqs {
			if req.PutRequest != nil {
				s.items = append(s.items, req.PutRequest.Item)
			}
		}
	}
	return &dynamodb.BatchWriteItemOutput{}, nil
}

var _ kdynamo.DynamoClient = (*stubDynamo)(nil)

// --- tests ---

func makeItem(n int) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", n)},
	}
}

func TestScan(t *testing.T) {
	stub := &stubDynamo{}
	for i := 0; i < 5; i++ {
		stub.items = append(stub.items, makeItem(i))
	}

	got, err := kdynamo.Scan(stub, &dynamodb.ScanInput{TableName: aws.String("test")},
		func(item map[string]types.AttributeValue) (int, error) {
			v, ok := item["id"].(*types.AttributeValueMemberN)
			if !ok {
				return 0, fmt.Errorf("unexpected type")
			}
			var n int
			_, _ = fmt.Sscan(v.Value, &n)
			return n, nil
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
}

func TestQuery(t *testing.T) {
	stub := &stubDynamo{}
	for i := 0; i < 3; i++ {
		stub.items = append(stub.items, makeItem(i))
	}

	got, err := kdynamo.Query(stub, &dynamodb.QueryInput{TableName: aws.String("test")},
		func(item map[string]types.AttributeValue) (string, error) {
			v := item["id"].(*types.AttributeValueMemberN)
			return v.Value, nil
		},
	).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestBatchWrite(t *testing.T) {
	stub := &stubDynamo{}
	items := make([]int, 50) // 2 batches of 25
	for i := range items {
		items[i] = i
	}

	sink := kdynamo.BatchWrite(stub, "test", func(n int) (types.WriteRequest, error) {
		return types.WriteRequest{
			PutRequest: &types.PutRequest{
				Item: makeItem(n),
			},
		}, nil
	})
	if err := sink(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	if len(stub.items) != 50 {
		t.Fatalf("want 50 items in stub, got %d", len(stub.items))
	}
}

func TestScanTake(t *testing.T) {
	stub := &stubDynamo{}
	for i := 0; i < 10; i++ {
		stub.items = append(stub.items, makeItem(i))
	}

	got, err := kdynamo.Scan(stub, &dynamodb.ScanInput{TableName: aws.String("test")},
		func(item map[string]types.AttributeValue) (int, error) {
			v := item["id"].(*types.AttributeValueMemberN)
			var n int
			_, _ = fmt.Sscan(v.Value, &n)
			return n, nil
		},
	).Take(3).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestIntegration(t *testing.T) {
	if os.Getenv("TEST_DYNAMO_TABLE") == "" {
		t.Skip("TEST_DYNAMO_TABLE not set; skipping DynamoDB integration tests")
	}
}
