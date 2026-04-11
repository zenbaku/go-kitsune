// Example: inspector — observe channel backpressure with BufferHook.
//
// This example is intentionally excluded from TestExamples (interactive/infinite).
// Run it manually:
//
//	go run ./v2/examples/inspector
//
// Demonstrates: BufferHook, BufferStatus, live buffer polling
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// bufferInspector implements BufferHook and polls channel occupancies.
type bufferInspector struct {
	query atomic.Pointer[func() []kitsune.BufferStatus]
	done  chan struct{}
}

func (b *bufferInspector) OnStageStart(context.Context, string)                 {}
func (b *bufferInspector) OnItem(context.Context, string, time.Duration, error) {}
func (b *bufferInspector) OnStageDone(context.Context, string, int64, int64)    {}
func (b *bufferInspector) OnBuffers(query func() []kitsune.BufferStatus) {
	b.query.Store(&query)
}

func (b *bufferInspector) poll() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			qp := b.query.Load()
			if qp == nil {
				continue
			}
			statuses := (*qp)()
			fmt.Print("\033[2J\033[H") // clear screen
			fmt.Println("=== Buffer Inspector ===")
			for _, s := range statuses {
				pct := 0
				if s.Capacity > 0 {
					pct = s.Length * 100 / s.Capacity
				}
				bar := ""
				for i := 0; i < 20; i++ {
					if i < pct/5 {
						bar += "█"
					} else {
						bar += "░"
					}
				}
				fmt.Printf("  %-20s [%s] %3d%% (%d/%d)\n",
					s.Stage, bar, pct, s.Length, s.Capacity)
			}
		case <-b.done:
			return
		}
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	inspector := &bufferInspector{done: make(chan struct{})}
	go inspector.poll()
	defer close(inspector.done)

	// Build a pipeline with a slow consumer so buffers fill up visibly.
	var counter int64
	src := kitsune.Map(
		kitsune.Ticker(1*time.Millisecond),
		func(_ context.Context, _ time.Time) (int64, error) {
			n := counter
			counter++
			return n, nil
		},
		kitsune.WithName("counter"),
	)

	processed := kitsune.Map(src,
		func(_ context.Context, n int64) (int64, error) {
			return n * n, nil
		},
		kitsune.Buffer(64),
		kitsune.WithName("square"),
	)

	slow := kitsune.Map(processed,
		func(_ context.Context, n int64) (int64, error) {
			time.Sleep(5 * time.Millisecond) // slow consumer creates backpressure
			return n, nil
		},
		kitsune.Buffer(32),
		kitsune.WithName("slow-consumer"),
	)

	slow.Drain().Run(ctx, kitsune.WithHook(inspector)) //nolint
	fmt.Println("\ndone")
}
