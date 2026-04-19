package kitsune_test

import (
	"context"
	"runtime"
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

	runner := kitsune.Batch(p, kitsune.BatchCount(batchSize)).ForEach(func(_ context.Context, batch []int) error {
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

// TestCooperativeDrainGoroutineCount verifies that a linear Map->Take(1)->ForEach
// pipeline tears down without spawning drain goroutines.
// With the old DrainChan approach, each stage spawns one goroutine on teardown
// (stages+1 goroutines above baseline). With cooperative drain, zero extra
// goroutines should remain after Run returns.
func TestCooperativeDrainGoroutineCount(t *testing.T) {
	const stages = 10

	// Allow any test-harness goroutines to settle before taking the baseline.
	runtime.Gosched()
	baseline := runtime.NumGoroutine()

	p := kitsune.Repeatedly(func() int { return 1 })
	for range stages {
		p = kitsune.Map(p, func(_ context.Context, v int) (int, error) {
			return v, nil
		})
	}

	err := kitsune.Take(p, 1).ForEach(func(_ context.Context, _ int) error {
		return nil
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give any residual goroutines a scheduling window to exit.
	runtime.Gosched()

	after := runtime.NumGoroutine()
	leaked := after - baseline
	// Allow 1 for any test-framework background goroutine.
	if leaked > 1 {
		t.Errorf("goroutine burst: %d goroutines remain above baseline after teardown (baseline=%d after=%d); expected <=1",
			leaked, baseline, after)
	}
}

// TestCooperativeDrainMerge verifies that a Merge(src1, src2)->Take(1) pipeline
// tears down without leaking goroutines (multi-input fan-in).
func TestCooperativeDrainMerge(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 50 {
		src1 := kitsune.Repeatedly(func() int { return 1 })
		src2 := kitsune.Repeatedly(func() int { return 2 })
		p := kitsune.Take(kitsune.Merge(src1, src2), 1)
		_, err := kitsune.Collect(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	// Allow 2 goroutines above baseline (test framework overhead).
	if after-baseline > 2 {
		t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
	}
}

// TestCooperativeDrainBroadcast verifies that a Broadcast(src, 2)->Take(1)/Take(1)
// pipeline tears down without leaking goroutines (fan-out with two consumers).
func TestCooperativeDrainBroadcast(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 50 {
		src := kitsune.Repeatedly(func() int { return 1 })
		branches := kitsune.Broadcast(src, 2)
		p0 := kitsune.Take(branches[0], 1)
		p1 := kitsune.Take(branches[1], 1)
		runner, err := kitsune.MergeRunners(p0.Drain(), p1.Drain())
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	if after-baseline > 2 {
		t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
	}
}

// TestCooperativeDrainSource verifies that a FromSlice(1000 items)->Take(1) pipeline
// tears down without leaking goroutines (source with early exit).
func TestCooperativeDrainSource(t *testing.T) {
	baseline := runtime.NumGoroutine()
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	for range 50 {
		p := kitsune.Take(kitsune.FromSlice(items), 1)
		_, err := kitsune.Collect(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	if after-baseline > 2 {
		t.Errorf("goroutine leak: before=%d after=%d delta=%d", baseline, after, after-baseline)
	}
}
