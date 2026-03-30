// Example: pause — suspend and resume a running pipeline.
//
// Demonstrates: RunHandle.Pause, RunHandle.Resume, RunHandle.Paused,
// NewGate, WithPauseGate
//
// Shows how to temporarily stop sources from emitting new items without
// cancelling the pipeline or losing in-flight work. Useful for maintenance
// windows, downstream backpressure, and rate-adaptive ingestion.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/jonathan/go-kitsune"
)

func main() {
	runHandleExample()
	externalGateExample()
}

// runHandleExample shows the primary pattern: pause/resume via RunHandle.
func runHandleExample() {
	fmt.Println("=== RunHandle pause/resume ===")

	src := kitsune.NewChannel[int](32)
	var processed atomic.Int64

	h := kitsune.Map(src.Source(),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
	).ForEach(func(_ context.Context, _ int) error {
		processed.Add(1)
		return nil
	}).RunAsync(context.Background())

	// Send first batch and let it flow.
	for i := 1; i <= 5; i++ {
		src.Send(context.Background(), i) //nolint:errcheck
	}
	time.Sleep(50 * time.Millisecond)
	fmt.Printf("after first batch:  processed=%d, paused=%v\n", processed.Load(), h.Paused())

	// Pause: sources stop emitting. Items sent now queue in the channel buffer.
	h.Pause()
	before := processed.Load()
	for i := 6; i <= 10; i++ {
		src.Send(context.Background(), i) //nolint:errcheck
	}
	time.Sleep(50 * time.Millisecond)
	fmt.Printf("while paused:       processed=%d, paused=%v\n", processed.Load(), h.Paused())
	if processed.Load() > before {
		// Items can still arrive if they were in-flight before the pause took effect.
		fmt.Println("(note: a few in-flight items may have drained through before the gate closed)")
	}

	// Resume: queued items drain through.
	h.Resume()
	src.Close()

	if err := h.Wait(); err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("after close+resume: processed=%d, paused=%v\n", processed.Load(), h.Paused())
}

// externalGateExample shows WithPauseGate for use with blocking Runner.Run.
func externalGateExample() {
	fmt.Println("\n=== WithPauseGate (external gate) ===")

	gate := kitsune.NewGate()
	gate.Pause() // start paused — sources emit nothing until Resume

	var processed atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	pipelineDone := make(chan error, 1)

	go func() {
		counter := func(yield func(int) bool) {
			for i := 0; ; i++ {
				if !yield(i) {
					return
				}
			}
		}
		pipelineDone <- kitsune.FromIter(counter).
			ForEach(func(_ context.Context, _ int) error {
				processed.Add(1)
				return nil
			}).Run(ctx, kitsune.WithPauseGate(gate))
	}()

	time.Sleep(30 * time.Millisecond)
	fmt.Printf("gate paused from start: processed=%d\n", processed.Load())

	gate.Resume()
	time.Sleep(30 * time.Millisecond)
	fmt.Printf("after resume:           processed>0 = %v\n", processed.Load() > 0)

	cancel()
	<-pipelineDone
	fmt.Println("pipeline stopped cleanly")
}
