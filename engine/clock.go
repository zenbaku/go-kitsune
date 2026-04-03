package engine

import "time"

// Clock abstracts time operations for deterministic testing.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
}

// Timer is the subset of *time.Timer used by operators.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Ticker is the subset of *time.Ticker used by operators.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock is the default Clock implementation that delegates to the standard library.
type RealClock struct{}

func (RealClock) Now() time.Time                        { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (RealClock) NewTimer(d time.Duration) Timer {
	t := time.NewTimer(d)
	return &realTimer{t: t}
}

func (RealClock) NewTicker(d time.Duration) Ticker {
	t := time.NewTicker(d)
	return &realTicker{t: t}
}

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time           { return r.t.C }
func (r *realTimer) Stop() bool                   { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool   { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()              { r.t.Stop() }
