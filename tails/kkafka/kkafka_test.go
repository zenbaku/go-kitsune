package kkafka_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	kafka "github.com/segmentio/kafka-go"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kkafka"
)

// brokerAddr returns the test broker address from the environment,
// or skips the test if it is not configured.
func brokerAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("KAFKA_BROKER")
	if addr == "" {
		t.Skip("KAFKA_BROKER not set — skipping integration test")
	}
	return addr
}

func TestConsumeProduce(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-" + t.Name()

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	type Msg struct {
		ID int `json:"id"`
	}

	// Write 3 messages.
	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	producer := kitsune.FromSlice([]Msg{{1}, {2}, {3}})
	if err := producer.ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	// Read them back.
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var results []Msg
	pipe := kkafka.Consume[Msg](reader, func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	})
	pipe.Take(3).ForEach(func(_ context.Context, v Msg) error {
		results = append(results, v)
		return nil
	}).Run(ctx) //nolint:errcheck
	cancel()

	if len(results) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(results))
	}
}
