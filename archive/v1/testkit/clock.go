package testkit

import (
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

// TestClock is a virtual clock for deterministic testing of time-sensitive pipelines.
// Advance virtual time explicitly with Advance; timers and tickers fire based on
// virtual time, not wall-clock time.
type TestClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*testTimer
	tickers []*testTicker
}

// NewTestClock returns a TestClock with virtual time starting at 2020-01-01 00:00:00 UTC.
func NewTestClock() *TestClock {
	return &TestClock{
		now: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// Now returns the current virtual time.
func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves virtual time forward by d, firing all timers and tickers
// whose deadlines have been reached, in chronological order.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now

	var timerChs []chan time.Time
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.deadline.After(now) {
			t.fired = true
			timerChs = append(timerChs, t.ch)
		}
	}

	type tickerFire struct {
		ch chan time.Time
		t  time.Time
	}
	var tickFires []tickerFire
	for _, tk := range c.tickers {
		if tk.stopped {
			continue
		}
		for !tk.next.After(now) {
			tickFires = append(tickFires, tickerFire{tk.ch, tk.next})
			tk.next = tk.next.Add(tk.period)
		}
	}
	c.mu.Unlock()

	for _, ch := range timerChs {
		select {
		case ch <- now:
		default:
		}
	}
	for _, tf := range tickFires {
		select {
		case tf.ch <- tf.t:
		default:
		}
	}
}

// After returns a channel that receives the current virtual time after d.
func (c *TestClock) After(d time.Duration) <-chan time.Time {
	t := c.NewTimer(d)
	return t.C()
}

// NewTimer creates a new virtual timer that fires after d virtual time has passed.
func (c *TestClock) NewTimer(d time.Duration) engine.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	t := &testTimer{
		clock:    c,
		ch:       ch,
		deadline: c.now.Add(d),
	}
	c.timers = append(c.timers, t)
	return t
}

// NewTicker creates a new virtual ticker that fires every d virtual time.
func (c *TestClock) NewTicker(d time.Duration) engine.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	tk := &testTicker{
		ch:     ch,
		period: d,
		next:   c.now.Add(d),
		clock:  c,
	}
	c.tickers = append(c.tickers, tk)
	return tk
}

type testTimer struct {
	clock    *TestClock
	ch       chan time.Time
	deadline time.Time
	stopped  bool
	fired    bool
}

func (t *testTimer) C() <-chan time.Time { return t.ch }

func (t *testTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (t *testTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := !t.stopped && !t.fired
	// Drain the channel if it has a value.
	select {
	case <-t.ch:
	default:
	}
	t.stopped = false
	t.fired = false
	t.deadline = t.clock.now.Add(d)
	return wasActive
}

type testTicker struct {
	ch      chan time.Time
	period  time.Duration
	next    time.Time
	clock   *TestClock
	stopped bool
}

func (t *testTicker) C() <-chan time.Time { return t.ch }

func (t *testTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
}
