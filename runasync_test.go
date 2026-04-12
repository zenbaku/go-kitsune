package kitsune_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func TestRunAsync_PauseResume(t *testing.T) {
	ch := kitsune.NewChannel[int](16)
	var (
		mu  sync.Mutex
		got []int
	)
	h := kitsune.Map(ch.Source(), func(_ context.Context, n int) (int, error) { return n, nil }).
		ForEach(func(_ context.Context, n int) error {
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
			return nil
		}).Build().RunAsync(context.Background())

	// Send a few items and let them flow.
	_ = ch.Send(context.Background(), 1)
	_ = ch.Send(context.Background(), 2)
	_ = ch.Send(context.Background(), 3)
	time.Sleep(50 * time.Millisecond)

	// Pause and record how many items arrived so far.
	h.Pause()
	if !h.Paused() {
		t.Fatal("expected Paused() == true")
	}
	mu.Lock()
	countAtPause := len(got)
	mu.Unlock()

	// Items pushed while paused should not reach the sink.
	_ = ch.Send(context.Background(), 4)
	_ = ch.Send(context.Background(), 5)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countWhilePaused := len(got)
	mu.Unlock()
	if countWhilePaused > countAtPause {
		t.Fatalf("items flowed while paused: had %d at pause, %d after", countAtPause, countWhilePaused)
	}

	// Resume and let buffered items drain.
	h.Resume()
	if h.Paused() {
		t.Fatal("expected Paused() == false after Resume")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countAfterResume := len(got)
	mu.Unlock()
	if countAfterResume <= countAtPause {
		t.Fatalf("expected more items after resume, got %d (was %d at pause)", countAfterResume, countAtPause)
	}

	ch.Close()
	if err := h.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAsync_PauseContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Infinite source.
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	h := kitsune.FromIter(counter).ForEach(func(_ context.Context, _ int) error {
		return nil
	}).Build().RunAsync(ctx)

	h.Pause()
	time.Sleep(20 * time.Millisecond)

	// Cancelling the context while paused should unblock and exit cleanly.
	cancel()
	select {
	case <-h.Done():
		// clean exit
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not exit after context cancel while paused")
	}
	// nil or context.Canceled are both acceptable.
	if err := h.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAsync_PausedState(t *testing.T) {
	gate := kitsune.NewGate()
	if gate.Paused() {
		t.Fatal("new gate should not be paused")
	}
	gate.Pause()
	if !gate.Paused() {
		t.Fatal("gate should be paused")
	}
	gate.Resume()
	if gate.Paused() {
		t.Fatal("gate should be open after Resume")
	}
}

func TestRunAsync_ReturnsNilOnSuccess(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	h := p.ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())
	if err := h.Wait(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestRunAsync_PropagatesError(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	mapped := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	})
	h := mapped.ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())
	if err := h.Wait(); !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestRunAsync_Done_ClosesOnCompletion(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	h := p.ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())
	select {
	case <-h.Done():
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Done() channel did not close")
	}
}

func TestRunAsync_Err_AfterDone(t *testing.T) {
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	select {
	case <-h.Done():
		if err := h.Err(); !errors.Is(err, boom) {
			t.Fatalf("expected boom, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close")
	}
}

func TestRunAsync_ConcurrentWait(t *testing.T) {
	// Two concurrent Wait() callers must both receive the result without
	// either blocking forever. This was impossible with the old errCh design.
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = h.Wait()
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("one or both concurrent Wait() calls blocked forever")
	}
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("caller %d: expected boom, got %v", i, err)
		}
	}
}

func TestRunAsync_ErrNonBlocking(t *testing.T) {
	// Err() before Done() closes must return nil (non-blocking).
	// Err() after Done() closes must return the pipeline error.
	boom := errors.New("boom")
	p := kitsune.FromSlice([]int{1})
	h := kitsune.Map(p, func(_ context.Context, _ int) (int, error) {
		return 0, boom
	}).ForEach(func(_ context.Context, _ int) error { return nil }).Build().RunAsync(context.Background())

	// Wait for completion.
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close")
	}

	// After completion: Err() returns the stored error.
	if err := h.Err(); !errors.Is(err, boom) {
		t.Fatalf("expected boom after Done(), got %v", err)
	}
	// Safe to call multiple times.
	if err := h.Err(); !errors.Is(err, boom) {
		t.Fatalf("second Err() call: expected boom, got %v", err)
	}
}

func TestWithPauseGate(t *testing.T) {
	gate := kitsune.NewGate()
	gate.Pause()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int64
	counter := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- kitsune.FromIter(counter).
			ForEach(func(_ context.Context, _ int) error {
				count.Add(1)
				return nil
			}).Run(ctx, kitsune.WithPauseGate(gate))
	}()

	time.Sleep(30 * time.Millisecond)
	if count.Load() > 0 {
		t.Fatal("items should not flow while gate is paused from the start")
	}

	gate.Resume()
	time.Sleep(30 * time.Millisecond)
	if count.Load() == 0 {
		t.Fatal("items should flow after gate resumed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not exit after context cancel")
	}
}
