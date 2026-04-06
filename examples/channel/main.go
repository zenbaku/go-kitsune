// Example: channel — push-based source with external producers.
//
// Demonstrates: NewChannel, Channel.Send, Channel.Close, RunAsync, RunHandle
package main

import (
	"context"
	"fmt"
	"sync"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// NewChannel creates a push-based source. The buffer absorbs short bursts
	// so producers don't block while the pipeline is busy.
	src := kitsune.NewChannel[int](16)

	doubled := kitsune.Map(src.Source(),
		func(_ context.Context, n int) (int, error) { return n * 2, nil })

	var mu sync.Mutex
	var results []int

	handle := doubled.ForEach(func(_ context.Context, n int) error {
		mu.Lock()
		results = append(results, n)
		mu.Unlock()
		return nil
	}).Build().RunAsync(ctx)

	// Push items from this goroutine while the pipeline runs in the background.
	for i := 1; i <= 10; i++ {
		if err := src.Send(ctx, i); err != nil {
			panic(err)
		}
	}
	src.Close() // signals end of input; pipeline drains and exits

	if err := handle.Wait(); err != nil {
		panic(err)
	}

	fmt.Println("results:", results)

	// --- TrySend: non-blocking push, drops if full ---

	src2 := kitsune.NewChannel[string](2)

	var collected []string
	h2 := src2.Source().ForEach(func(_ context.Context, s string) error {
		collected = append(collected, s)
		return nil
	}).Build().RunAsync(ctx)

	src2.TrySend("a")
	src2.TrySend("b")
	src2.TrySend("c") // may be dropped if buffer is full
	src2.Close()
	h2.Wait() //nolint

	fmt.Println("received:", collected)
}
