package kazeh_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kazeh"
)

// ---------------------------------------------------------------------------
// In-memory stub for PartitionClient (Consume tests)
// ---------------------------------------------------------------------------

type stubPartitionClient struct {
	mu     sync.Mutex
	events []*azeventhubs.ReceivedEventData
	pos    int
}

func (c *stubPartitionClient) ReceiveEvents(_ context.Context, count int, _ *azeventhubs.ReceiveEventsOptions) ([]*azeventhubs.ReceivedEventData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pos >= len(c.events) {
		return nil, nil
	}
	end := c.pos + count
	if end > len(c.events) {
		end = len(c.events)
	}
	batch := c.events[c.pos:end]
	c.pos = end
	return batch, nil
}

// newEvent is a test helper that builds a *azeventhubs.ReceivedEventData with
// a JSON-encoded body.
func newEvent(v any) *azeventhubs.ReceivedEventData {
	body, _ := json.Marshal(v)
	return &azeventhubs.ReceivedEventData{
		EventData: azeventhubs.EventData{Body: body},
	}
}

// ---------------------------------------------------------------------------
// Consume tests (no Event Hubs connection required)
// ---------------------------------------------------------------------------

func TestConsume(t *testing.T) {
	type record struct{ Value string }

	client := &stubPartitionClient{
		events: []*azeventhubs.ReceivedEventData{
			newEvent(record{"hello"}),
			newEvent(record{"world"}),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	results, err := kazeh.Consume(client, func(e *azeventhubs.ReceivedEventData) (record, error) {
		var r record
		return r, json.Unmarshal(e.Body, &r)
	}).Take(2).Collect(ctx)
	cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Value != "hello" || results[1].Value != "world" {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestConsumeComposable(t *testing.T) {
	client := &stubPartitionClient{
		events: []*azeventhubs.ReceivedEventData{
			newEvent(1), newEvent(2), newEvent(3),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	results, err := kitsune.Map(
		kazeh.Consume(client, func(e *azeventhubs.ReceivedEventData) (int, error) {
			var n int
			return n, json.Unmarshal(e.Body, &n)
		}).Take(3),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
	).Collect(ctx)
	cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 || results[0] != 2 || results[1] != 4 || results[2] != 6 {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestConsumeEmpty(t *testing.T) {
	client := &stubPartitionClient{}

	// A stub with no events and a context cancelled after Take(0) completes.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results, err := kazeh.Consume(client, func(e *azeventhubs.ReceivedEventData) (int, error) {
		return 0, nil
	}).Take(0).Collect(ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Integration tests (require TEST_EVENTHUBS_CONN_STR + TEST_EVENTHUBS_NAME)
// ---------------------------------------------------------------------------

func integrationClients(t *testing.T) (*azeventhubs.ProducerClient, *azeventhubs.ConsumerClient) {
	t.Helper()
	connStr := os.Getenv("TEST_EVENTHUBS_CONN_STR")
	hubName := os.Getenv("TEST_EVENTHUBS_NAME")
	if connStr == "" || hubName == "" {
		t.Skip("TEST_EVENTHUBS_CONN_STR / TEST_EVENTHUBS_NAME not set — skipping Event Hubs integration test")
	}
	producer, err := azeventhubs.NewProducerClientFromConnectionString(connStr, hubName, nil)
	if err != nil {
		t.Fatalf("NewProducerClient: %v", err)
	}
	consumer, err := azeventhubs.NewConsumerClientFromConnectionString(connStr, hubName, azeventhubs.DefaultConsumerGroup, nil)
	if err != nil {
		t.Fatalf("NewConsumerClient: %v", err)
	}
	return producer, consumer
}

func TestProduceIntegration(t *testing.T) {
	producer, _ := integrationClients(t)
	defer producer.Close(context.Background())

	err := kitsune.FromSlice([]string{"hello", "world"}).
		ForEach(kazeh.Produce(producer, func(s string) (*azeventhubs.EventData, error) {
			return &azeventhubs.EventData{Body: []byte(s)}, nil
		})).
		Run(context.Background())

	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
}

func TestProduceBatchIntegration(t *testing.T) {
	producer, _ := integrationClients(t)
	defer producer.Close(context.Background())

	err := kitsune.Batch(kitsune.FromSlice([]string{"a", "b", "c"}), kitsune.BatchCount(10)).
		ForEach(kazeh.ProduceBatch(producer, func(s string) (*azeventhubs.EventData, error) {
			return &azeventhubs.EventData{Body: []byte(s)}, nil
		})).
		Run(context.Background())

	if err != nil {
		t.Fatalf("ProduceBatch: %v", err)
	}
}
