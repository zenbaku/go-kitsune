package kjetstream_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune/tails/kjetstream"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func natsURL() string {
	if u := os.Getenv("NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func skipIfNoNATS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	nc, err := nats.Connect(natsURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("NATS not available (%v); set NATS_URL to run integration tests", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		t.Fatalf("jetstream.New: %v", err)
	}
	return nc, js
}

func TestFetch(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamName := "KJETSTREAM_FETCH_" + t.Name()
	subject := "kjetstream.fetch.test"

	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	// Publish 5 messages.
	for i := 0; i < 5; i++ {
		data, _ := json.Marshal(i)
		if _, err := js.Publish(ctx, subject, data); err != nil {
			t.Fatal(err)
		}
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:          "test-fetch",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := kjetstream.Fetch(cons, 10, time.Second, func(msg jetstream.Msg) (int, error) {
		var n int
		return n, json.Unmarshal(msg.Data(), &n)
	}).Take(5).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 items, got %d", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Errorf("item[%d]: want %d, got %d", i, i, v)
		}
	}
}

func TestFetchBytes(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamName := "KJETSTREAM_FETCHBYTES_" + t.Name()
	subject := "kjetstream.fetchbytes.test"

	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	for i := 0; i < 3; i++ {
		data, _ := json.Marshal(i)
		if _, err := js.Publish(ctx, subject, data); err != nil {
			t.Fatal(err)
		}
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:          "test-fetchbytes",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := kjetstream.FetchBytes(cons, 1<<20, time.Second, func(msg jetstream.Msg) (int, error) {
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

func TestOrderedConsume(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamName := "KJETSTREAM_ORDERED_" + t.Name()
	subject := "kjetstream.ordered.test"

	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	// Publish 4 messages.
	for i := 0; i < 4; i++ {
		data, _ := json.Marshal(i)
		if _, err := js.Publish(ctx, subject, data); err != nil {
			t.Fatal(err)
		}
	}

	got, err := kjetstream.OrderedConsume(js, streamName,
		jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{subject},
			DeliverPolicy:  jetstream.DeliverAllPolicy,
		},
		func(msg jetstream.Msg) (int, error) {
			var n int
			return n, json.Unmarshal(msg.Data(), &n)
		},
	).Take(4).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 items, got %d", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Errorf("item[%d]: want %d, got %d", i, i, v)
		}
	}
}

func TestWatchKV(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "kjetstream_TestWatchKV",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteKeyValue(context.Background(), "kjetstream_TestWatchKV") })

	// Put two keys before watching; they will be delivered as the initial snapshot.
	if _, err := kv.Put(ctx, "foo", []byte("bar")); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, "baz", []byte("qux")); err != nil {
		t.Fatal(err)
	}

	type kvEvent struct {
		Key   string
		Value string
	}

	got, err := kjetstream.WatchKV(kv, ">",
		func(e jetstream.KeyValueEntry) (kvEvent, error) {
			return kvEvent{Key: e.Key(), Value: string(e.Value())}, nil
		},
	).Take(2).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
}

func TestPublishAsync(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	streamName := "KJETSTREAM_ASYNC_" + t.Name()
	subject := "kjetstream.async.test"

	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), streamName) })

	sink, flush := kjetstream.PublishAsync[int](js, subject, 10, func(n int) ([]byte, error) {
		return json.Marshal(n)
	})

	// Call sink directly (simulating pipeline ForEach).
	for i := 0; i < 5; i++ {
		if err := sink(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := flush(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify messages were published by pulling them back.
	cons, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:          "verify-async",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := kjetstream.Fetch(cons, 10, time.Second, func(msg jetstream.Msg) (int, error) {
		var n int
		return n, json.Unmarshal(msg.Data(), &n)
	}).Take(5).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 items published, got %d", len(got))
	}
}

func TestPutKV(t *testing.T) {
	nc, js := skipIfNoNATS(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "kjetstream_TestPutKV",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteKeyValue(context.Background(), "kjetstream_TestPutKV") })

	type item struct {
		ID    string
		Value int
	}

	sink := kjetstream.PutKV(kv,
		func(it item) string { return it.ID },
		func(it item) ([]byte, error) { return json.Marshal(it) },
	)

	items := []item{{"a", 1}, {"b", 2}, {"c", 3}}
	for _, it := range items {
		if err := sink(ctx, it); err != nil {
			t.Fatal(err)
		}
	}

	// Verify key "b" was stored correctly.
	entry, err := kv.Get(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	var got item
	if err := json.Unmarshal(entry.Value(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != 2 {
		t.Fatalf("want Value=2, got %d", got.Value)
	}
}
