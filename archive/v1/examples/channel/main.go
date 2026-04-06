// Example: channel — push-based source with RunAsync.
//
// Demonstrates: NewChannel, Channel.Source, Channel.Send, Channel.Close, RunAsync
//
// Shows the "service pipeline pattern": an external producer feeds items into a
// long-lived pipeline without blocking. In production this producer would be an
// HTTP handler, a Kafka consumer, or any other event-driven source.
package main

import (
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// Create a buffered channel source.
	src := kitsune.NewChannel[string](16)

	// Build the pipeline from the channel source.
	p := src.Source()
	upper := kitsune.Map(p, func(_ context.Context, s string) (string, error) {
		return strings.ToUpper(strings.TrimSpace(s)), nil
	})

	// Start the pipeline in the background — it runs until src is closed.
	h := upper.ForEach(func(_ context.Context, s string) error {
		fmt.Println("processed:", s)
		return nil
	}).RunAsync(ctx)

	// Simulate an external producer pushing events.
	events := []string{"  hello  ", "  world  ", "  kitsune  "}
	for _, e := range events {
		if err := src.Send(ctx, e); err != nil {
			fmt.Println("send error:", err)
			return
		}
	}

	// Signal that no more items are coming. The pipeline drains and exits.
	src.Close()

	// Wait for the pipeline to finish.
	if err := h.Wait(); err != nil {
		fmt.Println("pipeline error:", err)
	}
}
