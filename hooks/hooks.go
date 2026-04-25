// Package hooks defines the shared hook interfaces for go-kitsune pipelines.
//
// Tails import only this module. Either engine (v1 or v2) can be swapped by
// changing a single line in the user's go.mod — the tail code is unchanged.
//
//	import "github.com/zenbaku/go-kitsune/hooks"
//
// Both engines re-export these types as aliases so user code only ever imports
// one module (the engine or this package, never both).
package hooks

import (
	"context"
	"time"
)

// Hook receives lifecycle and per-item events during pipeline execution.
// All methods are called from stage goroutines; implementations must be safe
// for concurrent use.
type Hook interface {
	// OnStageStart is called once when a stage goroutine begins.
	OnStageStart(ctx context.Context, stage string)
	// OnItem is called after each item is processed. dur is the wall-clock time
	// spent in the user-supplied function. err is non-nil when the item failed
	// (including skipped items).
	OnItem(ctx context.Context, stage string, dur time.Duration, err error)
	// OnStageDone is called once when a stage goroutine exits.
	OnStageDone(ctx context.Context, stage string, processed int64, errors int64)
}

// NoopHook silently discards all events. Embed it in partial implementations.
type NoopHook struct{}

func (NoopHook) OnStageStart(context.Context, string)                 {}
func (NoopHook) OnItem(context.Context, string, time.Duration, error) {}
func (NoopHook) OnStageDone(context.Context, string, int64, int64)    {}

// OverflowHook is an optional extension of [Hook] for buffer-drop events.
// Checked via type assertion at runtime — existing Hook implementations need
// not implement this.
type OverflowHook interface {
	OnDrop(ctx context.Context, stage string, item any)
}

// SupervisionHook is an optional extension of [Hook] for stage restart events.
// Checked via type assertion — existing Hook implementations need not implement this.
type SupervisionHook interface {
	OnStageRestart(ctx context.Context, stage string, attempt int, cause error)
}

// SampleHook is an optional extension of [Hook] for item value sampling.
// Called for approximately every N-th successfully processed item exiting an
// instrumented stage. The item value is the post-transform output (type-erased).
// Checked via type assertion — existing Hook implementations need not implement this.
type SampleHook interface {
	OnItemSample(ctx context.Context, stage string, item any)
}

// GraphHook is an optional extension of [Hook] for pipeline topology.
// Called once before execution begins with a snapshot of every compiled node.
// Checked via type assertion — existing Hook implementations need not implement this.
type GraphHook interface {
	OnGraph(nodes []GraphNode)
}

// BufferHook is an optional extension of [Hook] for observing channel backpressure.
// The engine calls OnBuffers once before execution with a query function that
// returns a snapshot of all inter-stage channel occupancies when invoked.
// Call the query periodically (e.g. every 250ms) to track fill levels over time.
// Checked via type assertion — existing Hook implementations need not implement this.
type BufferHook interface {
	OnBuffers(query func() []BufferStatus)
}

// GraphNode is a snapshot of one pipeline stage, passed to [GraphHook.OnGraph].
type GraphNode struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	Kind           string        `json:"kind"`
	Inputs         []int64       `json:"inputs"`
	Concurrency    int           `json:"concurrency,omitempty"`
	Buffer         int           `json:"buffer,omitempty"`
	Overflow       int           `json:"overflow,omitempty"`
	BatchSize      int           `json:"batch_size,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	HasRetry       bool          `json:"has_retry,omitempty"`
	HasSupervision bool          `json:"has_supervision,omitempty"`
	SegmentName    string        `json:"segment_name,omitempty"`
	IsEffect       bool          `json:"is_effect,omitempty"`
	EffectRequired bool          `json:"effect_required,omitempty"`
}

// BufferStatus reports the current fill level of one stage's output channel.
type BufferStatus struct {
	Stage    string `json:"stage"`
	Length   int    `json:"length"`   // current number of items in the channel
	Capacity int    `json:"capacity"` // total channel capacity
}
