package kpulsar_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/zenbaku/go-kitsune/tails/kpulsar"
)

func skipIfNoPulsar(t *testing.T) pulsar.Client {
	t.Helper()
	url := os.Getenv("PULSAR_URL")
	if url == "" {
		t.Skip("PULSAR_URL not set; skipping Pulsar integration tests")
	}
	client, err := pulsar.NewClient(pulsar.ClientOptions{
		URL:               url,
		ConnectionTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Skipf("pulsar.NewClient: %v", err)
	}
	return client
}

func TestConsumeProduceRoundTrip(t *testing.T) {
	client := skipIfNoPulsar(t)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	topic := "kpulsar-test-" + t.Name()

	producer, err := client.CreateProducer(pulsar.ProducerOptions{Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	consumer, err := client.Subscribe(pulsar.ConsumerOptions{
		Topic:            topic,
		SubscriptionName: "test-sub",
		Type:             pulsar.Exclusive,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	type msg struct {
		N int `json:"n"`
	}

	pub := kpulsar.Produce[msg](producer, func(m msg) (*pulsar.ProducerMessage, error) {
		b, err := json.Marshal(m)
		return &pulsar.ProducerMessage{Payload: b}, err
	})
	for i := 0; i < 3; i++ {
		if err := pub(ctx, msg{N: i}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := kpulsar.Consume(consumer, func(m pulsar.Message) (msg, error) {
		var v msg
		return v, json.Unmarshal(m.Payload(), &v)
	}).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}
