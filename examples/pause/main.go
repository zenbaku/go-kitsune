// Example: pause — temporarily stop a running pipeline without cancelling it.
//
// Demonstrates:
//   - RunAsync + RunHandle.Pause / Resume / Paused
//   - NewGate + WithPauseGate for use with blocking Runner.Run
//   - Behaviour during pause: sources block, in-flight items drain normally
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	ctx := context.Background()

	// --- RunAsync: pause/resume via RunHandle ---
	//
	// RunAsync runs the pipeline in the background and returns a RunHandle.
	// The handle exposes Pause, Resume, and Paused without any extra setup.

	fmt.Println("=== RunAsync: pause and resume via RunHandle ===")

	src := kitsune.NewChannel[int](4)
	processed := kitsune.Map(src.Source(), func(_ context.Context, n int) (string, error) {
		time.Sleep(10 * time.Millisecond) // simulate work
		return fmt.Sprintf("item-%02d", n), nil
	}, kitsune.WithName("process"))

	var received []string
	handle := processed.ForEach(func(_ context.Context, s string) error {
		received = append(received, s)
		return nil
	}).RunAsync(ctx)

	// Send items 1–5 while running normally.
	for i := 1; i <= 5; i++ {
		src.Send(ctx, i) //nolint
	}
	time.Sleep(100 * time.Millisecond) // let them process

	// Pause: sources block before the next send; in-flight items finish.
	handle.Pause()
	fmt.Printf("paused: %v\n", handle.Paused())

	// Items sent while paused will queue in the channel buffer but won't
	// be picked up by the pipeline until Resume is called.
	go func() {
		for i := 6; i <= 8; i++ {
			src.Send(ctx, i) //nolint
		}
	}()

	time.Sleep(80 * time.Millisecond) // nothing new should process during pause
	fmt.Printf("processed while paused: %d items total\n", len(received))

	// Resume: sources unblock and the buffered items flow through.
	handle.Resume()
	fmt.Printf("resumed: %v\n", handle.Paused())
	time.Sleep(100 * time.Millisecond)

	src.Close()
	if err := handle.Wait(); err != nil {
		panic(err)
	}
	fmt.Printf("total processed: %d items\n", len(received))
	fmt.Println("results:", received)

	// --- Gate + WithPauseGate: pause a blocking Runner.Run ---
	//
	// RunHandle requires RunAsync. To pause a pipeline that uses the blocking
	// Runner.Run, create a Gate externally and attach it with WithPauseGate.

	fmt.Println("\n=== Gate + WithPauseGate: pause a blocking Run ===")

	gate := kitsune.NewGate()

	nums := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	pipeline := kitsune.Map(nums, func(_ context.Context, n int) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return fmt.Sprintf("n=%d", n), nil
	}, kitsune.WithName("map"))

	var out []string
	r := pipeline.ForEach(func(_ context.Context, s string) error {
		out = append(out, s)
		return nil
	})

	// Pause after 30ms (roughly 3 items processed) then resume after 50ms.
	go func() {
		time.Sleep(30 * time.Millisecond)
		gate.Pause()
		fmt.Println("gate paused")
		time.Sleep(50 * time.Millisecond)
		gate.Resume()
		fmt.Println("gate resumed")
	}()

	if err := r.Run(ctx, kitsune.WithPauseGate(gate)); err != nil {
		panic(err)
	}
	fmt.Printf("total: %d items\n", len(out))
}
