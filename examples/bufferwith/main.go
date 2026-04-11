// Example: bufferwith — signal-driven buffering with an external closing selector.
//
// Demonstrates: BufferWith, NewChannel, Ticker
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Simulate a stream of events arriving over time.
	eventCh := kitsune.NewChannel[string](20)
	events := eventCh.Source()

	// A heartbeat ticker fires every 200ms and acts as the flush signal.
	heartbeat := kitsune.Ticker(200 * time.Millisecond)

	// BufferWith accumulates events and flushes them as a batch on each
	// heartbeat tick. Events that arrive between ticks are grouped together.
	batches := kitsune.BufferWith(events, heartbeat)

	// Produce events in the background at irregular intervals.
	go func() {
		sends := []struct {
			delay int // ms before send
			value string
		}{
			{50, "login"},
			{80, "click"},
			{120, "scroll"},
			// heartbeat fires around 200ms: flushes [login, click, scroll]
			{250, "purchase"},
			{300, "click"},
			// heartbeat fires around 400ms: flushes [purchase, click]
			{450, "logout"},
			// heartbeat fires around 600ms: flushes [logout]
		}
		start := time.Now()
		for _, s := range sends {
			time.Sleep(time.Duration(s.delay)*time.Millisecond - time.Since(start))
			eventCh.Send(ctx, s.value)
		}
		eventCh.Close()
	}()

	batchNum := 0
	err := batches.ForEach(func(_ context.Context, batch []string) error {
		batchNum++
		fmt.Printf("batch %d: %v\n", batchNum, batch)
		return nil
	}).Run(ctx)
	if err != nil && err != context.DeadlineExceeded {
		panic(err)
	}
}
