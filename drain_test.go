package kitsune_test

import (
	"context"
	"sync"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func TestWithDrainFlushesPartialBatch(t *testing.T) {
	// A batch stage should flush its partial buffer when context is cancelled
	// with a drain timeout, rather than dropping items.
	const batchSize = 10
	const totalItems = 5 // fewer than batchSize so the batch never auto-flushes

	ctx, cancel := context.WithCancel(context.Background())

	var received [][]int
	var mu sync.Mutex

	// Source emits totalItems, then parks until context is cancelled.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := range totalItems {
			if !yield(i) {
				return nil
			}
		}
		<-ctx.Done()
		return nil
	})

	runner := kitsune.Batch(p, batchSize).ForEach(func(_ context.Context, batch []int) error {
		mu.Lock()
		received = append(received, append([]int(nil), batch...))
		mu.Unlock()
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, kitsune.WithDrain(2*time.Second))
	}()

	// Let the source emit all items, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not drain within timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("expected partial batch to be flushed, got none")
	}
	total := 0
	for _, b := range received {
		total += len(b)
	}
	if total != totalItems {
		t.Fatalf("expected %d items total, got %d", totalItems, total)
	}
}

func TestWithDrainHardStop(t *testing.T) {
	// When the drain timeout expires, the pipeline should terminate even if
	// a stage is still busy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Source parks forever after emitting one item.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		<-ctx.Done()
		return nil
	})

	// Sink simulates slow work but respects context cancellation.
	runner := p.ForEach(func(ctx context.Context, _ int) error {
		select {
		case <-time.After(10 * time.Second): // much longer than drain timeout
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, kitsune.WithDrain(100*time.Millisecond))
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Pipeline terminated — hard stop worked.
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not hard-stop after drain timeout")
	}
}

func TestWithDrainTimerLeak(t *testing.T) {
	// This test verifies that runWithDrain does not leak a goroutine when the
	// drain completes before the timeout fires. Run with -race or check goroutine
	// count; here we simply exercise the path and rely on the race detector.
	ctx, cancel := context.WithCancel(context.Background())

	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		<-ctx.Done()
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- p.ForEach(func(_ context.Context, _ int) error { return nil }).
			Run(ctx, kitsune.WithDrain(10*time.Second)) // long timeout; should not fire
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not complete after context cancel")
	}
	// If time.After was used, its goroutine would still be running here for ~10s.
	// The race detector or goleak would surface this. The test is a correctness
	// guard; the goroutine leak itself is only reliably visible with goleak.
}

func TestWithDrainNormalCompletion(t *testing.T) {
	// WithDrain should not affect normal (non-cancelled) pipeline completion.
	results, err := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) { return v * 2, nil },
	).Collect(context.Background(), kitsune.WithDrain(5*time.Second))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}
