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
		} else {
			chans[ChannelKey{n.ID, 0}] = make(chan any, buf)
		}
	}
	return chans
}
