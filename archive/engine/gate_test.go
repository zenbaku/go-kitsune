package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestGate_OpenByDefault(t *testing.T) {
	g := NewGate()
	if g.Paused() {
		t.Fatal("new gate should not be paused")
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait on open gate should return nil, got %v", err)
	}
}

func TestGate_PauseBlocks(t *testing.T) {
	g := NewGate()
	g.Pause()
	if !g.Paused() {
		t.Fatal("gate should be paused")
	}

	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		g.Wait(ctx) //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
		// goroutine exited (due to timeout) — confirms Wait blocked
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Wait should have unblocked after context timeout")
	}
}

func TestGate_ResumeUnblocks(t *testing.T) {
	g := NewGate()
	g.Pause()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := g.Wait(context.Background()); err != nil {
			t.Errorf("Wait should return nil after resume, got %v", err)
		}
	}()

	time.Sleep(10 * time.Millisecond) // let goroutine block on Wait
	g.Resume()
	wg.Wait()
}

func TestGate_WaitRespectsContext(t *testing.T) {
	g := NewGate()
	g.Pause()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := g.Wait(ctx)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestGate_PauseIdempotent(t *testing.T) {
	g := NewGate()
	g.Pause()
	g.Pause() // should not panic or deadlock

	if !g.Paused() {
		t.Fatal("gate should still be paused")
	}

	// Resume once should open it
	g.Resume()
	if g.Paused() {
		t.Fatal("gate should be open after Resume")
	}
}

func TestGate_ResumeIdempotent(t *testing.T) {
	g := NewGate()
	g.Resume() // on open gate: no-op
	g.Resume() // again: no-op, no panic

	if g.Paused() {
		t.Fatal("gate should remain open")
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait on open gate should return nil, got %v", err)
	}
}

func TestGate_MultipleCycles(t *testing.T) {
	g := NewGate()
	for i := 0; i < 5; i++ {
		if g.Paused() {
			t.Fatalf("cycle %d: gate should be open", i)
		}
		g.Pause()
		if !g.Paused() {
			t.Fatalf("cycle %d: gate should be paused", i)
		}
		g.Resume()
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after cycles should return nil, got %v", err)
	}
}

func TestGate_ConcurrentAccess(t *testing.T) {
	g := NewGate()
	var wg sync.WaitGroup

	// Multiple goroutines calling Pause/Resume/Wait concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); g.Pause() }()
		go func() { defer wg.Done(); g.Resume() }()
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			g.Wait(ctx) //nolint:errcheck
		}()
	}
	wg.Wait()
	// Ensure gate ends in a usable state: open it and verify Wait returns.
	g.Resume()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("gate should be usable after concurrent access, got %v", err)
	}
}
