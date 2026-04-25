// Example: contextmapper: per-item context propagation without implementing ContextCarrier.
//
// Demonstrates: WithContextMapper, per-item trace context injection on Map stages.
//
// Real-world scenario: messages arriving from a queue carry opaque metadata
// (e.g. an OpenTelemetry trace context encoded in headers). The message struct
// is a third-party type that cannot implement ContextCarrier, so a mapper
// function extracts the context instead.
package main

import (
	"context"
	"fmt"

	kitsune "github.com/zenbaku/go-kitsune"
)

// traceKey is a private context key type to avoid collisions.
type traceKey struct{}

// QueueMessage simulates a third-party message type (e.g. from a Kafka library)
// that cannot implement ContextCarrier.
type QueueMessage struct {
	Body    string
	TraceID string // would normally be extracted from trace headers
}

func main() {
	ctx := context.Background()

	messages := []QueueMessage{
		{Body: "create-order", TraceID: "trace-abc123"},
		{Body: "update-inventory", TraceID: "trace-def456"},
		{Body: "send-notification", TraceID: "trace-ghi789"},
	}

	// WithContextMapper extracts a per-item context from each QueueMessage
	// without requiring QueueMessage to implement kitsune.ContextCarrier.
	traceMapper := func(m QueueMessage) context.Context {
		return context.WithValue(context.Background(), traceKey{}, m.TraceID)
	}

	source := kitsune.FromSlice(messages)

	processed := kitsune.Map(source,
		func(ctx context.Context, m QueueMessage) (string, error) {
			// The context passed here carries the trace ID from the mapper.
			traceID, _ := ctx.Value(traceKey{}).(string)
			return fmt.Sprintf("[%s] processed: %s", traceID, m.Body), nil
		},
		kitsune.WithContextMapper(traceMapper),
		kitsune.WithName("process-message"),
	)

	_, err := processed.ForEach(func(_ context.Context, result string) error {
		fmt.Println(result)
		return nil
	}).Run(ctx)
	if err != nil {
		panic(err)
	}
}
