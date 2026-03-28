package kmqtt_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kmqtt"
)

// --- in-memory MQTT stub ---

// stubMessage implements mqtt.Message.
type stubMessage struct {
	topic   string
	payload []byte
}

func (m *stubMessage) Duplicate() bool   { return false }
func (m *stubMessage) Qos() byte         { return 0 }
func (m *stubMessage) Retained() bool    { return false }
func (m *stubMessage) Topic() string     { return m.topic }
func (m *stubMessage) MessageID() uint16 { return 0 }
func (m *stubMessage) Payload() []byte   { return m.payload }
func (m *stubMessage) Ack()              {}

var _ mqtt.Message = (*stubMessage)(nil)

// stubToken implements mqtt.Token.
type stubToken struct{ err error }

func (t *stubToken) Wait() bool                      { return true }
func (t *stubToken) WaitTimeout(d time.Duration) bool { return true }
func (t *stubToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (t *stubToken) Error() error { return t.err }

var _ mqtt.Token = (*stubToken)(nil)

// stubClient implements mqtt.Client with an in-memory pub/sub bus.
type stubClient struct {
	mu          sync.Mutex
	subscribers map[string][]mqtt.MessageHandler
}

func newStubClient() *stubClient {
	return &stubClient{subscribers: make(map[string][]mqtt.MessageHandler)}
}

func (c *stubClient) IsConnected() bool            { return true }
func (c *stubClient) IsConnectionOpen() bool        { return true }
func (c *stubClient) Connect() mqtt.Token           { return &stubToken{} }
func (c *stubClient) Disconnect(quiesce uint)       {}
func (c *stubClient) OptionsReader() mqtt.ClientOptionsReader {
	return mqtt.NewOptionsReader(mqtt.NewClientOptions())
}

func (c *stubClient) Subscribe(topic string, qos byte, cb mqtt.MessageHandler) mqtt.Token {
	c.mu.Lock()
	c.subscribers[topic] = append(c.subscribers[topic], cb)
	c.mu.Unlock()
	return &stubToken{}
}

func (c *stubClient) SubscribeMultiple(filters map[string]byte, cb mqtt.MessageHandler) mqtt.Token {
	return &stubToken{}
}

func (c *stubClient) Unsubscribe(topics ...string) mqtt.Token {
	c.mu.Lock()
	for _, topic := range topics {
		delete(c.subscribers, topic)
	}
	c.mu.Unlock()
	return &stubToken{}
}

func (c *stubClient) Publish(topic string, qos byte, retained bool, payload any) mqtt.Token {
	c.mu.Lock()
	handlers := make([]mqtt.MessageHandler, len(c.subscribers[topic]))
	copy(handlers, c.subscribers[topic])
	c.mu.Unlock()
	var data []byte
	switch v := payload.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	}
	msg := &stubMessage{topic: topic, payload: data}
	for _, h := range handlers {
		h(c, msg)
	}
	return &stubToken{}
}

func (c *stubClient) AddRoute(topic string, cb mqtt.MessageHandler) {}

var _ mqtt.Client = (*stubClient)(nil)

// --- tests ---

func TestSubscribePublish(t *testing.T) {
	client := newStubClient()
	topic := "test/items"

	type item struct {
		N int `json:"n"`
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		pub := kmqtt.Publish[item](client, topic, 0, func(v item) ([]byte, error) {
			return json.Marshal(v)
		})
		for i := 0; i < 3; i++ {
			_ = pub(context.Background(), item{N: i})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := kitsune.Map(
		kmqtt.Subscribe(client, topic, 0, func(msg mqtt.Message) (item, error) {
			var v item
			return v, json.Unmarshal(msg.Payload(), &v)
		}),
		func(_ context.Context, v item) (int, error) { return v.N, nil },
	).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestIntegration(t *testing.T) {
	broker := os.Getenv("MQTT_BROKER")
	if broker == "" {
		t.Skip("MQTT_BROKER not set; skipping MQTT integration tests")
	}

	opts := mqtt.NewClientOptions().AddBroker(broker)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		t.Skipf("mqtt connect: %v", token.Error())
	}
	defer client.Disconnect(250)

	topic := fmt.Sprintf("kmqtt/test/%d", time.Now().UnixNano())

	go func() {
		time.Sleep(50 * time.Millisecond)
		pub := kmqtt.Publish[int](client, topic, 0, func(n int) ([]byte, error) {
			return json.Marshal(n)
		})
		for i := 0; i < 3; i++ {
			_ = pub(context.Background(), i)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := kmqtt.Subscribe(client, topic, 0, func(msg mqtt.Message) (int, error) {
		var n int
		return n, json.Unmarshal(msg.Payload(), &n)
	}).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}
