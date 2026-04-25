package kamqp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/zenbaku/go-kitsune/tails/kamqp"
)

// --- stubs ---

// stubAcknowledger records Ack and Nack calls so tests can assert delivery
// acknowledgement behaviour without a live broker.
type stubAcknowledger struct {
	mu    sync.Mutex
	acks  []uint64
	nacks []nackRecord
}

type nackRecord struct {
	tag     uint64
	requeue bool
}

func (a *stubAcknowledger) Ack(tag uint64, multiple bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks = append(a.acks, tag)
	return nil
}

func (a *stubAcknowledger) Nack(tag uint64, multiple, requeue bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nacks = append(a.nacks, nackRecord{tag, requeue})
	return nil
}

func (a *stubAcknowledger) Reject(tag uint64, requeue bool) error { return nil }

// stubConsumer feeds a fixed set of deliveries via a buffered channel.
type stubConsumer struct {
	ch chan amqp.Delivery
}

func newStubConsumer(deliveries []amqp.Delivery) *stubConsumer {
	ch := make(chan amqp.Delivery, len(deliveries))
	for _, d := range deliveries {
		ch <- d
	}
	return &stubConsumer{ch: ch}
}

func (s *stubConsumer) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	return s.ch, nil
}

// mkDelivery constructs an amqp.Delivery with the given body and an
// in-process Acknowledger so Ack/Nack work without a real channel.
func mkDelivery(ack *stubAcknowledger, tag uint64, body []byte) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: ack,
		DeliveryTag:  tag,
		Body:         body,
	}
}

// stubPublisher records every published message.
type stubPublisher struct {
	mu      sync.Mutex
	records []publishRecord
}

type publishRecord struct {
	exchange, key string
	msg           amqp.Publishing
}

func (p *stubPublisher) PublishWithContext(_ context.Context, exchange, key string, _, _ bool, msg amqp.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, publishRecord{exchange, key, msg})
	return nil
}

// --- helpers ---

type event struct {
	N int `json:"n"`
}

func unmarshalEvent(d *amqp.Delivery) (event, error) {
	var e event
	return e, json.Unmarshal(d.Body, &e)
}

func marshalEvent(e event) (amqp.Publishing, error) {
	b, err := json.Marshal(e)
	return amqp.Publishing{ContentType: "application/json", Body: b}, err
}

func eventBody(n int) []byte {
	b, _ := json.Marshal(event{N: n})
	return b
}

// --- Consume tests ---

func TestConsume_YieldsAndAcks(t *testing.T) {
	ack := &stubAcknowledger{}
	deliveries := []amqp.Delivery{
		mkDelivery(ack, 1, eventBody(1)),
		mkDelivery(ack, 2, eventBody(2)),
		mkDelivery(ack, 3, eventBody(3)),
	}
	stub := newStubConsumer(deliveries)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := kamqp.Consume(stub, "q", unmarshalEvent).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d", len(got))
	}

	ack.mu.Lock()
	defer ack.mu.Unlock()
	if len(ack.acks) != 3 {
		t.Fatalf("want 3 acks, got %d", len(ack.acks))
	}
	if len(ack.nacks) != 0 {
		t.Fatalf("want 0 nacks, got %d", len(ack.nacks))
	}
}

func TestConsume_UnmarshalErrorNacks(t *testing.T) {
	ack := &stubAcknowledger{}
	stub := newStubConsumer([]amqp.Delivery{
		mkDelivery(ack, 1, []byte("not-json")),
	})

	ctx := context.Background()
	_, err := kamqp.Consume(stub, "q", unmarshalEvent).Collect(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	ack.mu.Lock()
	defer ack.mu.Unlock()
	if len(ack.nacks) != 1 {
		t.Fatalf("want 1 nack, got %d", len(ack.nacks))
	}
	if !ack.nacks[0].requeue {
		t.Fatal("want requeue=true by default")
	}
}

func TestConsume_WithRequeueOnNackFalse(t *testing.T) {
	ack := &stubAcknowledger{}
	stub := newStubConsumer([]amqp.Delivery{
		mkDelivery(ack, 1, []byte("not-json")),
	})

	ctx := context.Background()
	_, err := kamqp.Consume(stub, "q", unmarshalEvent, kamqp.WithRequeueOnNack(false)).Collect(ctx)
	if err == nil {
		t.Fatal("expected error")
	}

	ack.mu.Lock()
	defer ack.mu.Unlock()
	if len(ack.nacks) != 1 || ack.nacks[0].requeue {
		t.Fatalf("want 1 nack with requeue=false, got %+v", ack.nacks)
	}
}

func TestConsume_DeliveriesChannelClosed(t *testing.T) {
	stub := newStubConsumer(nil)
	close(stub.ch) // broker-initiated close

	ctx := context.Background()
	got, err := kamqp.Consume(stub, "q", unmarshalEvent).Collect(ctx)
	if err != nil {
		t.Fatalf("expected clean termination, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 items, got %d", len(got))
	}
}

func TestConsume_ContextCancelled(t *testing.T) {
	// Consumer with no deliveries; blocks until context is cancelled.
	stub := newStubConsumer(nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := kamqp.Consume(stub, "q", unmarshalEvent).Collect(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConsume_EarlyStopAcksInFlight(t *testing.T) {
	ack := &stubAcknowledger{}
	deliveries := []amqp.Delivery{
		mkDelivery(ack, 1, eventBody(1)),
		mkDelivery(ack, 2, eventBody(2)),
		mkDelivery(ack, 3, eventBody(3)),
	}
	stub := newStubConsumer(deliveries)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Take(1) causes early stop after the first item. Depending on pipeline
	// scheduling, the goroutine may process more than 1 item before observing
	// the downstream stop, but every decoded item must be acked (never nacked).
	got, err := kamqp.Consume(stub, "q", unmarshalEvent).Take(1).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 item, got %d", len(got))
	}

	ack.mu.Lock()
	defer ack.mu.Unlock()
	if len(ack.acks) < 1 {
		t.Fatalf("want at least 1 ack, got %d", len(ack.acks))
	}
	if len(ack.nacks) != 0 {
		t.Fatalf("want 0 nacks, got %d", len(ack.nacks))
	}
}

func TestConsume_AutoAckSkipsAcknowledger(t *testing.T) {
	ack := &stubAcknowledger{}
	stub := newStubConsumer([]amqp.Delivery{
		mkDelivery(ack, 1, eventBody(1)),
		mkDelivery(ack, 2, eventBody(2)),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := kamqp.Consume(stub, "q", unmarshalEvent, kamqp.WithAutoAck()).Take(2).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}

	ack.mu.Lock()
	defer ack.mu.Unlock()
	if len(ack.acks) != 0 {
		t.Fatalf("with autoAck, want 0 manual acks, got %d", len(ack.acks))
	}
}

// --- Publish tests ---

func TestPublish_SendsToExchange(t *testing.T) {
	pub := &stubPublisher{}
	sink := kamqp.Publish[event](pub, "my.exchange", "my.key", marshalEvent)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := sink(ctx, event{N: i}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.records) != 3 {
		t.Fatalf("want 3 records, got %d", len(pub.records))
	}
	for _, r := range pub.records {
		if r.exchange != "my.exchange" {
			t.Errorf("exchange: want my.exchange, got %q", r.exchange)
		}
		if r.key != "my.key" {
			t.Errorf("key: want my.key, got %q", r.key)
		}
	}
}

func TestPublish_MarshalError(t *testing.T) {
	pub := &stubPublisher{}
	wantErr := errors.New("marshal failed")
	sink := kamqp.Publish[event](pub, "", "q", func(e event) (amqp.Publishing, error) {
		return amqp.Publishing{}, wantErr
	})

	err := sink(context.Background(), event{N: 1})
	if !errors.Is(err, wantErr) {
		t.Fatalf("want %v, got %v", wantErr, err)
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.records) != 0 {
		t.Fatal("nothing should have been published on marshal error")
	}
}

// --- Integration tests (skipped unless a broker is reachable) ---

func amqpURL() string {
	if u := os.Getenv("AMQP_URL"); u != "" {
		return u
	}
	return "amqp://guest:guest@localhost:5672/"
}

func skipIfNoAMQP(t *testing.T) *amqp.Connection {
	t.Helper()
	conn, err := amqp.DialConfig(amqpURL(), amqp.Config{
		Dial: amqp.DefaultDial(2 * time.Second),
	})
	if err != nil {
		t.Skipf("AMQP broker not available (%v); set AMQP_URL to run integration tests", err)
	}
	return conn
}

func TestConsumePublish_Integration(t *testing.T) {
	conn := skipIfNoAMQP(t)
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()

	queueName := "kamqp.test." + t.Name()
	q, err := ch.QueueDeclare(queueName, false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = ch.QueueDelete(q.Name, false, false, false)
	})

	// Publish 3 messages.
	sink := kamqp.Publish[event](ch, "", q.Name, marshalEvent)
	for i := 0; i < 3; i++ {
		if err := sink(context.Background(), event{N: i}); err != nil {
			t.Fatal(err)
		}
	}

	// Consume them back.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := kamqp.Consume(ch, q.Name, unmarshalEvent).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d", len(got))
	}
}
