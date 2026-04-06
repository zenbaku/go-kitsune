// Example: testclock — deterministic testing of time-sensitive pipelines.
//
// Demonstrates: WithClock + testkit.NewTestClock() as a drop-in replacement
// for the real wall-clock inside Debounce (and Window, Batch, Throttle).
// Advancing virtual time with clock.Advance() triggers timer-based flushes
// without any real sleeps, making tests instant and 100 % deterministic.
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

func main() {
	// ── Debounce with a TestClock ─────────────────────────────────────────────
	//
	// Debounce suppresses a burst of items and only emits the last one after
	// the quiet period elapses.  With a real clock you would need to sleep for
	// the full quiet period; with a TestClock you call clock.Advance() instead.

	clock := testkit.NewTestClock()
	const quietPeriod = 5 * time.Second // large value — no real sleep needed

	src := kitsune.NewChannel[string](10)

	debounced := kitsune.Debounce(src.Source(), quietPeriod, kitsune.WithClock(clock))

	// Collect runs the pipeline concurrently; we drive it from this goroutine.
	resultCh := make(chan []string, 1)
	go func() {
		items, _ := debounced.Collect(context.Background())
		resultCh <- items
	}()

	// Allow the background goroutine to start and enter its idle select.
	time.Sleep(5 * time.Millisecond)

	// Simulate a user typing quickly — three keystrokes in rapid succession.
	fmt.Println("Sending burst: \"hel\", \"hell\", \"hello\"")
	_ = src.Send(context.Background(), "hel")
	_ = src.Send(context.Background(), "hell")
	_ = src.Send(context.Background(), "hello")

	// Small pause so all three items are buffered before we advance the clock.
	time.Sleep(5 * time.Millisecond)

	// Advance virtual time past the quiet period — the debounce fires immediately.
	fmt.Printf("Advancing virtual clock by %v (no real sleep)…\n", quietPeriod)
	clock.Advance(quietPeriod)

	// Allow the flush to propagate before we close.
	time.Sleep(10 * time.Millisecond)

	src.Close()
	results := <-resultCh

	fmt.Printf("Debounced output: %v  (only the last keystroke survived)\n", results)

	// ── Compare: without TestClock you would need: ────────────────────────────
	//
	//   time.Sleep(quietPeriod)   // blocks for 5 seconds — not suitable for tests
	//
	// With TestClock the advance is instantaneous and the test stays fast.
}
