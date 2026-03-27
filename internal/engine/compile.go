package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Interfaces — used by the public kitsune package to inject behavior
// ---------------------------------------------------------------------------

// Hook receives lifecycle and per-item events during pipeline execution.
type Hook interface {
	OnStageStart(ctx context.Context, stage string)
	OnItem(ctx context.Context, stage string, dur time.Duration, err error)
	OnStageDone(ctx context.Context, stage string, processed int64, errors int64)
}

// NoopHook silently discards all events.
type NoopHook struct{}

func (NoopHook) OnStageStart(context.Context, string)                 {}
func (NoopHook) OnItem(context.Context, string, time.Duration, error) {}
func (NoopHook) OnStageDone(context.Context, string, int64, int64)    {}

// ErrorHandler decides what to do when a stage function returns an error.
type ErrorHandler interface {
	Handle(err error, attempt int) ErrorAction
	Backoff() func(attempt int) time.Duration
}

// ErrorAction is the decision returned by an [ErrorHandler].
type ErrorAction int

const (
	ActionHalt  ErrorAction = iota // stop the pipeline
	ActionSkip                     // drop item, continue
	ActionRetry                    // retry with backoff
)

// DefaultHandler halts on any error.
type DefaultHandler struct{}

func (DefaultHandler) Handle(error, int) ErrorAction            { return ActionHalt }
func (DefaultHandler) Backoff() func(attempt int) time.Duration { return nil }

// ErrSkipped is an internal sentinel indicating an item was dropped.
var ErrSkipped = errors.New("kitsune: item skipped")

// ---------------------------------------------------------------------------
// Graph validation
// ---------------------------------------------------------------------------

// Validate checks the graph for structural errors before execution.
func Validate(g *Graph) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.Nodes) == 0 {
		return errors.New("kitsune: pipeline has no stages")
	}

	// Single-consumer rule: each (node, port) output is consumed at most once.
	consumed := make(map[ChannelKey]bool)
	hasSink := false

	for _, n := range g.Nodes {
		if n.Kind == Sink {
			hasSink = true
		}
		for _, ref := range n.Inputs {
			key := ChannelKey{ref.Node, ref.Port}
			if consumed[key] {
				return fmt.Errorf(
					"kitsune: output of stage %d (port %d) is consumed by multiple stages — use Partition or Broadcast for fan-out",
					ref.Node, ref.Port,
				)
			}
			consumed[key] = true
		}
	}

	if !hasSink {
		return errors.New("kitsune: pipeline has no terminal stage (ForEach, Drain, or Collect)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Channel wiring
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Supervision
// ---------------------------------------------------------------------------

// SupervisionPolicy configures per-stage restart and panic-recovery behavior.
// Zero value = no restarts, panics propagate (identical to v1 behavior).
type SupervisionPolicy struct {
	MaxRestarts int                     // 0 = no restarts (default)
	Window      time.Duration           // reset counter after quiet period; 0 = never reset
	Backoff     func(int) time.Duration // delay between restarts (nil = no delay)
	OnPanic     PanicAction
}

// HasSupervision reports whether any supervision is active.
func (p SupervisionPolicy) HasSupervision() bool {
	return p.MaxRestarts > 0 || p.OnPanic != PanicPropagate
}

// PanicAction configures what happens when a stage goroutine panics.
type PanicAction int

const (
	PanicPropagate PanicAction = iota // re-panic (default — existing behavior)
	PanicRestart                      // treat panic as a restartable error
	PanicSkip                         // recover and continue; the panicking item is lost
)

// SupervisionHook is an optional extension of Hook for restart events.
// Checked via type assertion — existing Hook implementations need not implement this.
type SupervisionHook interface {
	OnStageRestart(ctx context.Context, stage string, attempt int, cause error)
}

// OverflowHook is an optional extension of Hook for drop events.
// Checked via type assertion — existing Hook implementations need not implement this.
type OverflowHook interface {
	OnDrop(ctx context.Context, stage string, item any)
}

// SampleHook is an optional extension of Hook for item value sampling.
// Called for approximately every 10th successful item exiting an instrumented
// stage. The item value is the post-transform output (type-erased).
// Checked via type assertion — existing Hook implementations need not implement this.
type SampleHook interface {
	OnItemSample(ctx context.Context, stage string, item any)
}

// GraphHook is an optional extension of Hook for graph topology.
// Called once before execution begins with a snapshot of every compiled node.
// Checked via type assertion — existing Hook implementations need not implement this.
type GraphHook interface {
	OnGraph(nodes []GraphNode)
}

// BufferStatus reports the current fill level of one stage's output channel.
type BufferStatus struct {
	Stage    string
	Length   int // current number of items in the channel (len)
	Capacity int // total channel capacity (cap)
}

// BufferHook is an optional extension of Hook for observing channel backpressure.
// The engine calls OnBuffers once before execution with a query function that
// returns a snapshot of all inter-stage channel occupancies when invoked.
// Call the function periodically (e.g. every 250ms) to track fill levels over time.
// Checked via type assertion — existing Hook implementations need not implement this.
type BufferHook interface {
	OnBuffers(query func() []BufferStatus)
}

// GraphNode is a snapshot of one pipeline stage passed to [GraphHook.OnGraph].
type GraphNode struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Inputs      []int  `json:"inputs"`
	Concurrency int    `json:"concurrency,omitempty"`
	Buffer      int    `json:"buffer,omitempty"`
	Overflow    int    `json:"overflow,omitempty"`
}

// CreateChannels allocates bounded channels for every non-sink output port.
func CreateChannels(g *Graph) map[ChannelKey]chan any {
	chans := make(map[ChannelKey]chan any)

	for _, n := range g.Nodes {
		if n.Kind == Sink {
			continue
		}
		buf := n.Buffer
		if buf <= 0 {
			buf = DefaultBuffer
		}
		if n.Kind == Partition {
			chans[ChannelKey{n.ID, 0}] = make(chan any, buf) // match
			chans[ChannelKey{n.ID, 1}] = make(chan any, buf) // rest
		} else if n.Kind == BroadcastNode {
			for i := range n.BroadcastN {
				chans[ChannelKey{n.ID, i}] = make(chan any, buf)
			}
		} else {
			chans[ChannelKey{n.ID, 0}] = make(chan any, buf)
		}
	}
	return chans
}

// CreateOutboxes wraps each output channel in an Outbox with the appropriate
// overflow strategy. Consumers still read from the plain chan any in chans.
func CreateOutboxes(g *Graph, chans map[ChannelKey]chan any, hook Hook) map[ChannelKey]Outbox {
	outboxes := make(map[ChannelKey]Outbox, len(chans))
	for _, n := range g.Nodes {
		if n.Kind == Sink {
			continue
		}
		name := n.Name
		if name == "" {
			name = kindName(n.Kind)
		}
		overflow := n.Overflow
		if n.Kind == Partition {
			outboxes[ChannelKey{n.ID, 0}] = NewOutbox(chans[ChannelKey{n.ID, 0}], overflow, hook, name)
			outboxes[ChannelKey{n.ID, 1}] = NewOutbox(chans[ChannelKey{n.ID, 1}], overflow, hook, name)
		} else if n.Kind == BroadcastNode {
			for i := range n.BroadcastN {
				outboxes[ChannelKey{n.ID, i}] = NewOutbox(chans[ChannelKey{n.ID, i}], overflow, hook, name)
			}
		} else {
			outboxes[ChannelKey{n.ID, 0}] = NewOutbox(chans[ChannelKey{n.ID, 0}], overflow, hook, name)
		}
	}
	return outboxes
}
