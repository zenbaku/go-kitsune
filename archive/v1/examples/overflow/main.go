// Example: overflow — mailbox overflow strategies for real-time pipelines.
//
// Demonstrates: Overflow, DropNewest, DropOldest, Buffer, OverflowHook
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// dropLogger implements Hook + OverflowHook to log and count every dropped item.
type dropLogger struct {
	kitsune.Hook
	count atomic.Int64
}

func (h *dropLogger) OnDrop(_ context.Context, stage string, _ any) {
	h.count.Add(1)
	fmt.Printf("[drop] stage=%q total_dropped=%d\n", stage, h.count.Load())
}

func main() {
	// --- DropNewest: incoming items are discarded when the buffer is full ---
	fmt.Println("=== DropNewest ===")
	{
		const n = 100
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		p := kitsune.FromSlice(items)
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
		h := &dropLogger{Hook: kitsune.LogHook(logger)}

		var received atomic.Int64
		err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			return v, nil // fast producer
		}, kitsune.Buffer(4), kitsune.Overflow(kitsune.DropNewest), kitsune.WithName("fast-map")).
			ForEach(func(_ context.Context, v int) error {
				time.Sleep(5 * time.Millisecond) // slow consumer
				received.Add(1)
				return nil
			}).Run(context.Background(), kitsune.WithHook(h))
		if err != nil {
			panic(err)
		}
		fmt.Printf("received %d/%d items (%d dropped)\n\n", received.Load(), n, h.count.Load())
	}

	// --- DropOldest: oldest buffered item is evicted to make room ---
	fmt.Println("=== DropOldest ===")
	{
		const n = 50
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		p := kitsune.FromSlice(items)
		var received []int
		err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			return v, nil // fast producer
		}, kitsune.Buffer(4), kitsune.Overflow(kitsune.DropOldest), kitsune.WithName("drop-oldest")).
			ForEach(func(_ context.Context, v int) error {
				time.Sleep(3 * time.Millisecond) // slow consumer
				received = append(received, v)
				return nil
			}).Run(context.Background())
		if err != nil {
			panic(err)
		}
		fmt.Printf("received %d/%d items — last item: %d (newest always survives)\n\n",
			len(received), n, received[len(received)-1])
	}

	// --- Default (Block): all items delivered, pipeline applies backpressure ---
	fmt.Println("=== Block (default) ===")
	{
		const n = 20
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		p := kitsune.FromSlice(items)
		var received atomic.Int64
		err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			return v, nil
		}, kitsune.Buffer(2)). // no Overflow → Block
					ForEach(func(_ context.Context, v int) error {
				received.Add(1)
				return nil
			}).Run(context.Background())
		if err != nil {
			panic(err)
		}
		fmt.Printf("received %d/%d items (all delivered via backpressure)\n", received.Load(), n)
	}
}
