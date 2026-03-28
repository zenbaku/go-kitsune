package kkinesis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/jonathan/go-kitsune/tails/kkinesis"
)

// --- in-memory Kinesis stub ---

type stubKinesis struct {
	mu      sync.Mutex
	records []types.Record
	pos     int
}

func (s *stubKinesis) GetShardIterator(_ context.Context, _ *kinesis.GetShardIteratorInput, _ ...func(*kinesis.Options)) (*kinesis.GetShardIteratorOutput, error) {
	return &kinesis.GetShardIteratorOutput{ShardIterator: aws.String("iter-0")}, nil
}

func (s *stubKinesis) GetRecords(ctx context.Context, params *kinesis.GetRecordsInput, _ ...func(*kinesis.Options)) (*kinesis.GetRecordsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= len(s.records) {
		// Block until context is done (simulates end of shard / no new records).
		s.mu.Unlock()
		<-ctx.Done()
		s.mu.Lock()
		return &kinesis.GetRecordsOutput{
			Records:              nil,
			NextShardIterator:    nil,
			MillisBehindLatest:   aws.Int64(0),
		}, nil
	}
	limit := int(aws.ToInt32(params.Limit))
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	end := s.pos + limit
	if end > len(s.records) {
		end = len(s.records)
	}
	batch := s.records[s.pos:end]
	s.pos = end
	millis := int64(0)
	if s.pos < len(s.records) {
		millis = 1000
	}
	return &kinesis.GetRecordsOutput{
		Records:            batch,
		NextShardIterator:  aws.String(fmt.Sprintf("iter-%d", s.pos)),
		MillisBehindLatest: aws.Int64(millis),
	}, nil
}

func (s *stubKinesis) PutRecords(_ context.Context, params *kinesis.PutRecordsInput, _ ...func(*kinesis.Options)) (*kinesis.PutRecordsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]types.PutRecordsResultEntry, len(params.Records))
	for i, e := range params.Records {
		seq := fmt.Sprintf("seq-%d", len(s.records)+i)
		s.records = append(s.records, types.Record{
			Data:           e.Data,
			SequenceNumber: aws.String(seq),
			PartitionKey:   e.PartitionKey,
		})
		results[i] = types.PutRecordsResultEntry{SequenceNumber: aws.String(seq)}
	}
	return &kinesis.PutRecordsOutput{Records: results, FailedRecordCount: aws.Int32(0)}, nil
}

var _ kkinesis.KinesisClient = (*stubKinesis)(nil)

// --- tests ---

func TestConsumeAndProduce(t *testing.T) {
	stub := &stubKinesis{}

	// Pre-load records via Produce.
	items := []string{"a", "b", "c", "d", "e"}
	sink := kkinesis.Produce[string](stub, "test-stream", func(s string) (types.PutRecordsRequestEntry, error) {
		return types.PutRecordsRequestEntry{
			Data:         []byte(s),
			PartitionKey: aws.String("key"),
		}, nil
	})
	if err := sink(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	if len(stub.records) != 5 {
		t.Fatalf("want 5 records in stub, got %d", len(stub.records))
	}

	// Reset position and consume them back.
	stub.pos = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got, err := kkinesis.Consume(stub, "test-stream", "shard-0",
		types.ShardIteratorTypeTrimHorizon, "",
		func(r types.Record) (string, error) {
			return string(r.Data), nil
		},
	).Take(5).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	for i, v := range got {
		if v != items[i] {
			t.Errorf("item[%d]: want %q, got %q", i, items[i], v)
		}
	}
}

func TestProduceBatch(t *testing.T) {
	stub := &stubKinesis{}
	items := make([]int, 150)
	for i := range items {
		items[i] = i
	}
	sink := kkinesis.Produce[int](stub, "test-stream", func(n int) (types.PutRecordsRequestEntry, error) {
		b, err := json.Marshal(n)
		return types.PutRecordsRequestEntry{Data: b, PartitionKey: aws.String("k")}, err
	})
	if err := sink(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	if len(stub.records) != 150 {
		t.Fatalf("want 150 records, got %d", len(stub.records))
	}
}

func TestIntegration(t *testing.T) {
	if os.Getenv("TEST_KINESIS_STREAM") == "" {
		t.Skip("TEST_KINESIS_STREAM not set; skipping Kinesis integration tests")
	}
}
