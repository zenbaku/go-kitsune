package ksqs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/zenbaku/go-kitsune/tails/ksqs"
)

// --- in-memory SQS stub ---

type stubQueue struct {
	mu       sync.Mutex
	messages []types.Message
	nextID   int
}

func (q *stubQueue) ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	// Simulate long-polling: block until messages are available or context done.
	for {
		q.mu.Lock()
		n := int(params.MaxNumberOfMessages)
		if n <= 0 || n > 10 {
			n = 10
		}
		if n > len(q.messages) {
			n = len(q.messages)
		}
		if n > 0 {
			out := make([]types.Message, n)
			copy(out, q.messages[:n])
			q.mu.Unlock()
			return &sqs.ReceiveMessageOutput{Messages: out}, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return &sqs.ReceiveMessageOutput{}, nil
		case <-time.After(time.Millisecond):
		}
	}
}

func (q *stubQueue) DeleteMessage(_ context.Context, params *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	handle := aws.ToString(params.ReceiptHandle)
	for i, m := range q.messages {
		if aws.ToString(m.ReceiptHandle) == handle {
			q.messages = append(q.messages[:i], q.messages[i+1:]...)
			break
		}
	}
	return &sqs.DeleteMessageOutput{}, nil
}

func (q *stubQueue) SendMessage(_ context.Context, params *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	id := fmt.Sprintf("msg-%d", q.nextID)
	q.messages = append(q.messages, types.Message{
		MessageId:     aws.String(id),
		ReceiptHandle: aws.String(id),
		Body:          params.MessageBody,
	})
	return &sqs.SendMessageOutput{MessageId: aws.String(id)}, nil
}

func (q *stubQueue) SendMessageBatch(_ context.Context, params *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var successful []types.SendMessageBatchResultEntry
	for _, e := range params.Entries {
		q.nextID++
		id := fmt.Sprintf("msg-%d", q.nextID)
		q.messages = append(q.messages, types.Message{
			MessageId:     aws.String(id),
			ReceiptHandle: aws.String(id),
			Body:          e.MessageBody,
		})
		successful = append(successful, types.SendMessageBatchResultEntry{
			Id:        e.Id,
			MessageId: aws.String(id),
		})
	}
	return &sqs.SendMessageBatchOutput{Successful: successful}, nil
}

var _ ksqs.SQSClient = (*stubQueue)(nil)

// --- tests ---

const queueURL = "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue"

func TestReceiveAndDelete(t *testing.T) {
	q := &stubQueue{}
	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(i)
		_, _ = q.SendMessage(context.Background(), &sqs.SendMessageInput{
			QueueUrl:    aws.String(queueURL),
			MessageBody: aws.String(string(body)),
		})
	}

	got, err := ksqs.Receive(q, queueURL, func(m types.Message) (int, error) {
		var n int
		return n, json.Unmarshal([]byte(aws.ToString(m.Body)), &n)
	}).Take(5).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	// All messages should have been deleted.
	q.mu.Lock()
	remaining := len(q.messages)
	q.mu.Unlock()
	if remaining != 0 {
		t.Errorf("want 0 remaining messages, got %d", remaining)
	}
}

func TestSend(t *testing.T) {
	q := &stubQueue{}
	sink := ksqs.Send[string](q, queueURL, func(s string) (string, error) { return s, nil })
	for _, msg := range []string{"a", "b", "c"} {
		if err := sink(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	q.mu.Lock()
	n := len(q.messages)
	q.mu.Unlock()
	if n != 3 {
		t.Fatalf("want 3 messages in queue, got %d", n)
	}
}

func TestSendBatch(t *testing.T) {
	q := &stubQueue{}
	items := make([]string, 25)
	for i := range items {
		items[i] = fmt.Sprintf("item-%d", i)
	}
	sink := ksqs.SendBatch[string](q, queueURL, func(s string) (string, error) { return s, nil })
	if err := sink(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	n := len(q.messages)
	q.mu.Unlock()
	if n != 25 {
		t.Fatalf("want 25 messages, got %d", n)
	}
}

// Integration test — gated by TEST_SQS_QUEUE_URL.
func TestIntegrationRoundTrip(t *testing.T) {
	qURL := os.Getenv("TEST_SQS_QUEUE_URL")
	if qURL == "" {
		t.Skip("TEST_SQS_QUEUE_URL not set; skipping SQS integration test")
	}
	// Integration setup omitted — requires AWS credentials and a real queue.
	_ = qURL
}
