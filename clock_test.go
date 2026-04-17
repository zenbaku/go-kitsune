package kitsune_test

import (
	"context"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// tickTimeout is the wall-clock deadline used when reading pipeline output
// after a clock.Advance call. It is large enough to survive a busy CI machine
// but small enough to give a clear error rather than hanging forever.
const tickTimeout = 2 * time.Second

// pipelineStartup is a brief sleep that allows a newly-spawned pipeline
// goroutine to reach its first select/timer-creation point before the test
// calls clock.Advance. Without this, Advance would fire before the ticker
// exists, and the first tick would be silently missed.
const pipelineStartup = 10 * time.Millisecond

// TestBatch_CountFlush verifies that Batch flushes on item count and emits a
// partial trailing batch when the source closes without filling the last batch.
func TestBatch_CountFlush(t *testing.T) {
	ctx := context.Background()
	ch := kitsune.NewChannel[int](10)

	batches := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Batch(ch.Source(), kitsune.BatchCount(3)).
			ForEach(func(_ context.Context, b []int) error {
				batches <- b
				return nil
			}).Run(ctx)
	}()

	for _, v := range []int{1, 2, 3} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	// First full batch should flush immediately.
	var b1 []int
	select {
	case b1 = <-batches:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for first batch")
	}
	if len(b1) != 3 {
		t.Errorf("batch 1: len=%d, want 3", len(b1))
	}

	for _, v := range []int{4, 5} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()

	// Partial batch should flush when source closes.
	var b2 []int
	select {
	case b2 = <-batches:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for second batch")
	}
	if len(b2) != 2 {
		t.Errorf("batch 2: len=%d, want 2", len(b2))
	}

	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestBatch_TestClock verifies that a partial batch is flushed when the
// BatchTimeout ticker fires via Advance — no real sleep required.
func TestBatch_TestClock(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	batches := make(chan []int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Batch(ch.Source(), kitsune.BatchCount(100),
			kitsune.BatchTimeout(5*time.Second),
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, b []int) error {
			batches <- b
			return nil
		}).Run(ctx)
	}()

	// Give the stage goroutine time to create the ticker before advancing.
	time.Sleep(pipelineStartup)

	// Send 2 items (well below the size=100 limit).
	for _, v := range []int{1, 2} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	// Let the goroutine consume both items before firing the ticker.
	time.Sleep(pipelineStartup)

	// Fire the ticker — the partial batch should flush.
	clock.Advance(5 * time.Second)

	var b1 []int
	select {
	case b1 = <-batches:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for first batch")
	}
	if len(b1) != 2 || b1[0] != 1 || b1[1] != 2 {
		t.Errorf("batch 1: got %v, want [1 2]", b1)
	}

	// Send 3 more items and fire again.
	for _, v := range []int{3, 4, 5} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(pipelineStartup)
	clock.Advance(5 * time.Second)

	var b2 []int
	select {
	case b2 = <-batches:
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for second batch")
	}
	if len(b2) != 3 {
		t.Errorf("batch 2: len=%d, want 3", len(b2))
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestThrottle_TestClock verifies that Throttle passes the first item, drops
// within-window items, then passes again after the window elapses (via Advance).
func TestThrottle_TestClock(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	items := make(chan int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Throttle(ch.Source(), 5*time.Second,
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, v int) error {
			items <- v
			return nil
		}).Run(ctx)
	}()

	// Item 1: first emission — always passes.
	if err := ch.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-items:
		if got != 1 {
			t.Errorf("item 1: got %d, want 1", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for item 1")
	}

	// Item 2: within the 5s window — should be dropped.
	if err := ch.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-items:
		t.Errorf("item 2 should have been throttled, but got %d", got)
	case <-time.After(20 * time.Millisecond):
		// Expected: nothing emitted.
	}

	// Advance past the window, then send item 3 — should pass.
	clock.Advance(5 * time.Second)
	if err := ch.Send(ctx, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-items:
		if got != 3 {
			t.Errorf("item 3: got %d, want 3", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for item 3")
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestDebounce_TestClock verifies that rapid items are coalesced and only the
// last one is emitted after the silence period fires via Advance.
func TestDebounce_TestClock(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	items := make(chan int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Debounce(ch.Source(), 3*time.Second,
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, v int) error {
			items <- v
			return nil
		}).Run(ctx)
	}()

	// Give the stage goroutine time to start before sending items.
	time.Sleep(pipelineStartup)

	// Send a burst: timer resets on each item; only the last should be emitted.
	for _, v := range []int{1, 2, 3} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for the goroutine to consume the burst and arm the silence timer.
	time.Sleep(pipelineStartup)

	// Fire the silence timer — last item (3) should be emitted.
	clock.Advance(3 * time.Second)

	select {
	case got := <-items:
		if got != 3 {
			t.Errorf("debounced value: got %d, want 3", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for debounced item")
	}

	// Send one more item, wait for goroutine to arm the timer, then advance.
	if err := ch.Send(ctx, 4); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)
	clock.Advance(3 * time.Second)

	select {
	case got := <-items:
		if got != 4 {
			t.Errorf("second debounced value: got %d, want 4", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for second debounced item")
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestTicker_TestClock verifies that Ticker emits one time.Time value per
// Advance(period) call, with no real sleep required.
func TestTicker_TestClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := testkit.NewTestClock()

	ticks := make(chan time.Time, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Take(
			kitsune.Ticker(time.Second, kitsune.WithClock(clock)),
			3,
		).ForEach(func(_ context.Context, ts time.Time) error {
			ticks <- ts
			return nil
		}).Run(ctx)
	}()

	// Wait for the pipeline goroutine to create the ticker before advancing.
	time.Sleep(pipelineStartup)

	for i := 0; i < 3; i++ {
		clock.Advance(time.Second)
		select {
		case <-ticks:
			// Got a tick — good.
		case <-time.After(tickTimeout):
			t.Fatalf("timeout waiting for tick %d", i+1)
		}
	}

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("pipeline error: %v", err)
		}
	case <-time.After(tickTimeout):
		t.Fatal("pipeline did not finish after 3 ticks")
	}
}

// ---------------------------------------------------------------------------
// Sample
// ---------------------------------------------------------------------------

// TestSample_EmitsLatestPerTick verifies that Sample emits the most-recently-
// seen item at each tick and resets the latch afterwards.
func TestSample_EmitsLatestPerTick(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	items := make(chan int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Sample(ch.Source(), 5*time.Second,
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, v int) error {
			items <- v
			return nil
		}).Run(ctx)
	}()

	time.Sleep(pipelineStartup)

	// Send three items before the first tick; only the last should be emitted.
	for _, v := range []int{1, 2, 3} {
		if err := ch.Send(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(pipelineStartup)
	clock.Advance(5 * time.Second)

	select {
	case got := <-items:
		if got != 3 {
			t.Errorf("first tick: got %d, want 3 (latest item)", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for first sample")
	}

	// Send one item, fire second tick — that item must be emitted.
	if err := ch.Send(ctx, 42); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)
	clock.Advance(5 * time.Second)

	select {
	case got := <-items:
		if got != 42 {
			t.Errorf("second tick: got %d, want 42", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for second sample")
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestSample_SkipsTickWhenNoItems verifies that ticks with no new items since
// the last emission produce no output.
func TestSample_SkipsTickWhenNoItems(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	items := make(chan int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Sample(ch.Source(), 5*time.Second,
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, v int) error {
			items <- v
			return nil
		}).Run(ctx)
	}()

	time.Sleep(pipelineStartup)

	// Send one item, advance once — should emit.
	if err := ch.Send(ctx, 7); err != nil {
		t.Fatal(err)
	}
	time.Sleep(pipelineStartup)
	clock.Advance(5 * time.Second)

	select {
	case got := <-items:
		if got != 7 {
			t.Errorf("got %d, want 7", got)
		}
	case <-time.After(tickTimeout):
		t.Fatal("timeout waiting for first sample")
	}

	// Advance again without sending any new item — nothing should be emitted.
	clock.Advance(5 * time.Second)
	select {
	case got := <-items:
		t.Errorf("expected no emission on empty tick, got %d", got)
	case <-time.After(30 * time.Millisecond):
		// Expected: nothing emitted.
	}

	ch.Close()
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
}

// TestSample_NoFlushOnClose verifies that when the upstream closes between
// ticks, the buffered latest item is NOT emitted (unlike Debounce).
func TestSample_NoFlushOnClose(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewTestClock()
	ch := kitsune.NewChannel[int](10)

	items := make(chan int, 10)
	done := make(chan error, 1)
	go func() {
		done <- kitsune.Sample(ch.Source(), 5*time.Second,
			kitsune.WithClock(clock),
		).ForEach(func(_ context.Context, v int) error {
			items <- v
			return nil
		}).Run(ctx)
	}()

	time.Sleep(pipelineStartup)

	// Send an item and close the source before the tick fires.
	if err := ch.Send(ctx, 99); err != nil {
		t.Fatal(err)
	}
	ch.Close()

	// Pipeline should complete without emitting the pending item.
	if err := <-done; err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	select {
	case got := <-items:
		t.Errorf("expected no emission on close, got %d", got)
	default:
	}
}
