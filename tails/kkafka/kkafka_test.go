package kkafka_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

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
	if _, err := producer.ForEach(sink).Run(context.Background()); err != nil {
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
	_, _ = pipe.Take(3).ForEach(func(_ context.Context, v Msg) error {
		results = append(results, v)
		return nil
	}).Run(ctx) //nolint:errcheck
	cancel()

	if len(results) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(results))
	}
}

func TestConsumeBatchSize(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-batchsize-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	// Produce 6 messages.
	msgs := make([]Msg, 6)
	for i := range msgs {
		msgs[i] = Msg{ID: i + 1}
	}
	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if _, err := kitsune.FromSlice(msgs).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// BatchSize(3) on 6 messages: two full commits of 3.
	var got []Msg
	_, err := kkafka.Consume[Msg](reader, unmarshal, kkafka.BatchSize(3)).
		Take(6).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(got))
	}
	for i, m := range got {
		if m.ID != i+1 {
			t.Errorf("msg[%d]: got ID %d, want %d", i, m.ID, i+1)
		}
	}
}

func TestConsumeBatchTimeout(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-batchtimeout-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if _, err := kitsune.FromSlice([]Msg{{1}, {2}, {3}}).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// BatchSize(100) would never fill naturally with only 3 messages.
	// BatchTimeout(10ms) flushes any partial batch within 10ms of the first
	// pending message, ensuring the pipeline doesn't stall at teardown.
	var got []Msg
	_, err := kkafka.Consume[Msg](reader, unmarshal,
		kkafka.BatchSize(100),
		kkafka.BatchTimeout(10*time.Millisecond),
	).
		Take(3).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
}

func TestConsumeBatchEarlyExit(t *testing.T) {
	broker := brokerAddr(t)
	topic := "kkafka-test-earlyexit-" + t.Name()

	type Msg struct{ ID int }

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(broker),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()

	msgs := make([]Msg, 6)
	for i := range msgs {
		msgs[i] = Msg{ID: i + 1}
	}
	sink := kkafka.Produce[Msg](writer, func(m Msg) (kafka.Message, error) {
		b, err := json.Marshal(m)
		return kafka.Message{Value: b}, err
	})
	if _, err := kitsune.FromSlice(msgs).ForEach(sink).Run(context.Background()); err != nil {
		t.Fatalf("produce: %v", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{broker},
		Topic:     topic,
		Partition: 0,
	})
	defer reader.Close()

	unmarshal := func(m kafka.Message) (Msg, error) {
		var v Msg
		return v, json.Unmarshal(m.Value, &v)
	}

	// Take(4) with BatchSize(3): first batch of 3 is committed; the 4th
	// message is pending when yield returns false. Pipeline must return nil
	// (not an error) and leave the 4th message uncommitted (at-least-once).
	var got []Msg
	_, err := kkafka.Consume[Msg](reader, unmarshal, kkafka.BatchSize(3)).
		Take(4).
		ForEach(func(_ context.Context, v Msg) error {
			got = append(got, v)
			return nil
		}).Run(context.Background())
	if err != nil {
		t.Fatalf("expected nil on early exit, got: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got))
	}
}
