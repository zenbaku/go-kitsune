package kazsb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kazsb"
)

// ---------------------------------------------------------------------------
// In-memory stubs
// ---------------------------------------------------------------------------

type stubReceiver struct {
	queue     []*azservicebus.ReceivedMessage
	completed []string // message IDs of completed messages
	pos       int
}

func (r *stubReceiver) ReceiveMessages(_ context.Context, max int, _ *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	if r.pos >= len(r.queue) {
		// Return empty to simulate idle queue; caller detects via context cancel.
		return nil, nil
	}
	end := r.pos + max
	if end > len(r.queue) {
		end = len(r.queue)
	}
	batch := r.queue[r.pos:end]
	r.pos = end
	return batch, nil
}

func (r *stubReceiver) CompleteMessage(_ context.Context, msg *azservicebus.ReceivedMessage, _ *azservicebus.CompleteMessageOptions) error {
	r.completed = append(r.completed, msg.MessageID)
	return nil
}

type stubSender struct {
	sent []*azservicebus.Message
}

func (s *stubSender) SendMessage(_ context.Context, msg *azservicebus.Message, _ *azservicebus.SendMessageOptions) error {
	s.sent = append(s.sent, msg)
	return nil
}

// newMsg is a test helper that builds a *azservicebus.ReceivedMessage with a
// JSON-encoded body and a message ID.
func newMsg(id string, v any) *azservicebus.ReceivedMessage {
	body, _ := json.Marshal(v)
	return &azservicebus.ReceivedMessage{
		Body:      body,
		MessageID: id,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReceive(t *testing.T) {
	type event struct{ Value string }

	recv := &stubReceiver{
		queue: []*azservicebus.ReceivedMessage{
			newMsg("1", event{"hello"}),
			newMsg("2", event{"world"}),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	results, err := kazsb.Receive(recv, func(msg *azservicebus.ReceivedMessage) (event, error) {
		var e event
		return e, json.Unmarshal(msg.Body, &e)
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

func TestReceiveCompletes(t *testing.T) {
	recv := &stubReceiver{
		queue: []*azservicebus.ReceivedMessage{
			newMsg("msg-1", "a"),
			newMsg("msg-2", "b"),
			newMsg("msg-3", "c"),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := kazsb.Receive(recv, func(msg *azservicebus.ReceivedMessage) (string, error) {
		var s string
		return s, json.Unmarshal(msg.Body, &s)
	}).Take(3).Collect(ctx)
	cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recv.completed) != 3 {
		t.Errorf("expected 3 completed messages, got %d: %v", len(recv.completed), recv.completed)
	}
}

func TestReceiveComposable(t *testing.T) {
	recv := &stubReceiver{
		queue: []*azservicebus.ReceivedMessage{
			newMsg("1", 1),
			newMsg("2", 2),
			newMsg("3", 3),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	results, err := kitsune.Map(
		kazsb.Receive(recv, func(msg *azservicebus.ReceivedMessage) (int, error) {
			var n int
			return n, json.Unmarshal(msg.Body, &n)
		}).Take(3),
		func(_ context.Context, n int) (int, error) { return n * 10, nil },
	).Collect(ctx)
	cancel()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 || results[0] != 10 || results[1] != 20 || results[2] != 30 {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestSend(t *testing.T) {
	sender := &stubSender{}

	_, err := kitsune.FromSlice([]string{"hello", "world"}).
		ForEach(kazsb.Send(sender, func(s string) (*azservicebus.Message, error) {
			return &azservicebus.Message{Body: []byte(s)}, nil
		})).
		Run(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("expected 2 messages sent, got %d", len(sender.sent))
	}
	if string(sender.sent[0].Body) != "hello" || string(sender.sent[1].Body) != "world" {
		t.Errorf("unexpected message bodies: %q %q",
			string(sender.sent[0].Body), string(sender.sent[1].Body))
	}
}

func TestSendMarshalError(t *testing.T) {
	sender := &stubSender{}
	sentinel := context.DeadlineExceeded // reuse a known error

	_, err := kitsune.FromSlice([]string{"a"}).
		ForEach(kazsb.Send(sender, func(_ string) (*azservicebus.Message, error) {
			return nil, sentinel
		})).
		Run(context.Background())

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(sender.sent) != 0 {
		t.Errorf("expected no messages sent on marshal error")
	}
}
