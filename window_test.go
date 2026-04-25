package kitsune_test

import (
	"context"
	"slices"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

func TestSessionWindow_MultipleSessions(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	const gap = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	sessions := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
			ForEach(func(_ context.Context, s []int) error {
				sessions <- s
				return nil
			}).Run(ctx)
		done <- err
	}()

	time.Sleep(pipelineStartup)

	// First session.
	for _, v := range []int{1, 2} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(pipelineStartup)

	// Advance past gap to flush first session.
	clock.Advance(gap)

	var s1 []int
	select {
	case s1 = <-sessions:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for first session")
	}
	if !slices.Equal(s1, []int{1, 2}) {
		t.Errorf("session[0] = %v, want [1 2]", s1)
	}

	// Second session.
	for _, v := range []int{3, 4} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(pipelineStartup)

	// Advance past gap to flush second session.
	clock.Advance(gap)

	var s2 []int
	select {
	case s2 = <-sessions:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for second session")
	}
	if !slices.Equal(s2, []int{3, 4}) {
		t.Errorf("session[1] = %v, want [3 4]", s2)
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

func TestSessionWindow_FlushOnClose(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	const gap = 10 * time.Second

	ch := kitsune.NewChannel[int](10)
	sessions := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
			ForEach(func(_ context.Context, s []int) error {
				sessions <- s
				return nil
			}).Run(ctx)
		done <- err
	}()

	time.Sleep(pipelineStartup)

	for _, v := range []int{1, 2, 3} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(pipelineStartup)

	// Close without advancing clock; flush must happen on source close.
	ch.Close()

	var s1 []int
	select {
	case s1 = <-sessions:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for session flush on close")
	}
	if !slices.Equal(s1, []int{1, 2, 3}) {
		t.Errorf("session[0] = %v, want [1 2 3]", s1)
	}

	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

func TestSessionWindow_Empty(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	const gap = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	sessions := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
			ForEach(func(_ context.Context, s []int) error {
				sessions <- s
				return nil
			}).Run(ctx)
		done <- err
	}()

	time.Sleep(pipelineStartup)
	ch.Close()

	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d: %v", len(sessions), <-sessions)
	}
}

func TestSessionWindow_ResetBehavior(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	const gap = 4 * time.Second
	const half = 2 * time.Second

	ch := kitsune.NewChannel[int](10)
	sessions := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		_, err := kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)).
			ForEach(func(_ context.Context, s []int) error {
				sessions <- s
				return nil
			}).Run(ctx)
		done <- err
	}()

	time.Sleep(pipelineStartup)

	// Send item 1.
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)

	// Advance by half the gap; timer should NOT fire yet.
	clock.Advance(half)
	time.Sleep(pipelineStartup)

	// Send item 2; this resets the gap timer.
	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)

	// Advance by half the gap again; only half elapsed since item 2, no flush.
	clock.Advance(half)
	time.Sleep(pipelineStartup)

	// Verify no flush has happened yet.
	select {
	case early := <-sessions:
		t.Fatalf("unexpected early flush: %v", early)
	default:
	}

	// Advance the remaining half; full gap elapsed since item 2, should flush.
	clock.Advance(half)

	var s1 []int
	select {
	case s1 = <-sessions:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for session after reset")
	}
	if !slices.Equal(s1, []int{1, 2}) {
		t.Errorf("session[0] = %v, want [1 2]", s1)
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

func TestSessionWindow_ContextCancel(t *testing.T) {
	clock := testkit.NewTestClock()
	const gap = 10 * time.Second

	ch := kitsune.NewChannel[int](10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := kitsune.Collect(ctx, kitsune.SessionWindow(ch.Source(), gap, kitsune.WithClock(clock)))
		done <- err
	}()

	time.Sleep(pipelineStartup)
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancel, got nil")
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for pipeline to exit after cancel")
	}
}

func TestSessionWindow_Panics(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})

	t.Run("zero gap", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected SessionWindow(p, 0) to panic")
			}
		}()
		kitsune.SessionWindow(src, 0)
	})

	t.Run("negative gap", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected SessionWindow(p, -1s) to panic")
			}
		}()
		kitsune.SessionWindow(src, -time.Second)
	})
}
