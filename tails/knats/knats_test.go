package knats_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune/tails/knats"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func natsURL() string {
	if u := os.Getenv("NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func skipIfNoNATS(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(natsURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("NATS not available (%v); set NATS_URL to run integration tests", err)
	}
	return nc
}

func TestSubscribePublish(t *testing.T) {
	nc := skipIfNoNATS(t)
	defer nc.Close()

	subject := "knats.test.subscribe." + t.Name()
	type msg struct {
		N int `json:"n"`
	}

	// Publish 3 messages in background after a brief delay to let subscriber start.
	go func() {
		time.Sleep(50 * time.Millisecond)
		pub := knats.Publish[msg](nc, subject, func(m msg) ([]byte, error) {
			return json.Marshal(m)
		})
		for i := 0; i < 3; i++ {
			_ = pub(context.Background(), msg{N: i})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := knats.Subscribe(nc, subject, func(m *nats.Msg) (msg, error) {
		var v msg
		return v, json.Unmarshal(m.Data, &v)
	}).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("want 3 messages, got %d", len(result))
	}
}

func TestJetStreamConsumePublish(t *testing.T) {
	nc := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	streamName := "KNATS_TEST_" + t.Name()
	subject := "knats.js.test"

	// Create (or reuse) a stream.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = js.DeleteStream(context.Background(), streamName)
	})

	// Publish 3 messages.
	pub := knats.JetStreamPublish[int](js, subject, func(n int) ([]byte, error) {
		return json.Marshal(n)
	})
	for i := 0; i < 3; i++ {
		if err := pub(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	// Consume them back.
	cons, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:          "test-consumer",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := knats.Consume(cons, func(msg jetstream.Msg) (int, error) {
		var n int
		return n, json.Unmarshal(msg.Data(), &n)
	}).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d", len(got))
	}
}
