package internal

import (
	"context"
	"sync"
)

// Gate is a reversible pause signal for pipeline sources.
// The zero value is an open (unpaused) gate.
// Create with [NewGate].
type Gate struct {
	mu     sync.Mutex
	waitCh chan struct{} // nil = open; non-nil = paused
}

// NewGate returns a new Gate in the open (unpaused) state.
func NewGate() *Gate { return &Gate{} }

// Pause blocks sources from emitting new items. Idempotent: calling Pause
// again while already paused has no effect.
func (g *Gate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waitCh == nil {
		g.waitCh = make(chan struct{})
	}
}

// Resume unblocks sources paused by [Pause]. Idempotent: calling Resume
// while not paused has no effect.
func (g *Gate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waitCh != nil {
		close(g.waitCh)
		g.waitCh = nil
	}
}

// Paused reports whether the gate is currently in the paused state.
func (g *Gate) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waitCh != nil
}

// Wait returns immediately when the gate is open. When the gate is paused it
// blocks until [Resume] is called or ctx is cancelled, in which case it
// returns ctx.Err().
func (g *Gate) Wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.waitCh
	g.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
