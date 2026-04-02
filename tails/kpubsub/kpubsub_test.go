package kpubsub_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/zenbaku/go-kitsune/tails/kpubsub"
)

// Integration tests require PUBSUB_PROJECT_ID and a running Pub/Sub emulator
// or real GCP project with a subscription configured.
func skipIfNoPubSub(t *testing.T) (*pubsub.Client, string) {
	t.Helper()
	projectID := os.Getenv("PUBSUB_PROJECT_ID")
	if projectID == "" {
		t.Skip("PUBSUB_PROJECT_ID not set; skipping Pub/Sub integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		t.Skipf("pubsub.NewClient: %v", err)
	}
	return client, projectID
}

func TestSubscribePublishRoundTrip(t *testing.T) {
	client, _ := skipIfNoPubSub(t)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	topicID := "knats-test-topic-" + t.Name()
	subID := "knats-test-sub-" + t.Name()

	topic, err := client.CreateTopic(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topic.Delete(context.Background()) })

	sub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Delete(context.Background()) })

	type msg struct {
		N int `json:"n"`
	}

	pub := kpubsub.Publish[msg](topic, func(m msg) (*pubsub.Message, error) {
		b, err := json.Marshal(m)
		return &pubsub.Message{Data: b}, err
	})
	for i := 0; i < 3; i++ {
		if err := pub(ctx, msg{N: i}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := kpubsub.Subscribe(sub, func(m *pubsub.Message) (msg, error) {
		var v msg
		return v, json.Unmarshal(m.Data, &v)
	}).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d", len(got))
	}
}
