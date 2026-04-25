package testkit

import (
	"context"
	"sync"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Event types
// ---------------------------------------------------------------------------

// StageStartEvent records a stage start notification.
type StageStartEvent struct {
	Stage string
}

// ItemEvent records a per-item notification.
type ItemEvent struct {
	Stage    string
	Duration time.Duration
	Err      error // nil for successful items
	Sample   any   // non-nil only for sample events
	IsSample bool
}

// DropEvent records an item-drop (overflow) notification.
type DropEvent struct {
	Stage string
	Item  any
}

// RestartEvent records a supervision restart notification.
type RestartEvent struct {
	Stage   string
	Attempt int
	Cause   error
}

// StageDoneEvent records a stage completion notification.
type StageDoneEvent struct {
	Stage     string
	Processed int64
	Errors    int64
}

// ---------------------------------------------------------------------------
// RecordingHook
// ---------------------------------------------------------------------------

// RecordingHook records all lifecycle events for inspection in tests.
// It implements [kitsune.Hook], [kitsune.OverflowHook], [kitsune.SupervisionHook],
// [kitsune.SampleHook], [kitsune.GraphHook], and [kitsune.BufferHook].
//
//	hook := &testkit.RecordingHook{}
//	runner.Run(ctx, kitsune.WithHook(hook))
//
//	// Inspect what happened:
//	fmt.Println(hook.Items())    // all per-item events
//	fmt.Println(hook.Errors())   // only events with non-nil error
//	fmt.Println(hook.Restarts()) // supervision restarts
type RecordingHook struct {
	mu       sync.Mutex
	starts   []StageStartEvent
	items    []ItemEvent
	dones    []StageDoneEvent
	drops    []DropEvent
	restarts []RestartEvent
	graph    []kitsune.GraphNode
}

// OnStageStart records a stage start event.
func (h *RecordingHook) OnStageStart(_ context.Context, stage string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, StageStartEvent{Stage: stage})
}

// OnItem records a per-item event.
func (h *RecordingHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = append(h.items, ItemEvent{Stage: stage, Duration: dur, Err: err})
}

// OnStageDone records a stage completion event.
func (h *RecordingHook) OnStageDone(_ context.Context, stage string, processed, errors int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dones = append(h.dones, StageDoneEvent{Stage: stage, Processed: processed, Errors: errors})
}

// OnDrop records a dropped item event.
func (h *RecordingHook) OnDrop(_ context.Context, stage string, item any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drops = append(h.drops, DropEvent{Stage: stage, Item: item})
}

// OnStageRestart records a stage restart event.
func (h *RecordingHook) OnStageRestart(_ context.Context, stage string, attempt int, cause error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restarts = append(h.restarts, RestartEvent{Stage: stage, Attempt: attempt, Cause: cause})
}

// OnItemSample records a sampled item as an ItemEvent with IsSample==true.
func (h *RecordingHook) OnItemSample(_ context.Context, stage string, item any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = append(h.items, ItemEvent{Stage: stage, Sample: item, IsSample: true})
}

// OnGraph records the graph topology.
func (h *RecordingHook) OnGraph(nodes []kitsune.GraphNode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph = append(h.graph[:0], nodes...)
}

// OnBuffers is a no-op for the recording hook (buffer polling is not recorded).
func (h *RecordingHook) OnBuffers(_ func() []kitsune.BufferStatus) {}

// ---------------------------------------------------------------------------
// Accessors: return copies to avoid races with ongoing recording.
// ---------------------------------------------------------------------------

// Starts returns all recorded stage-start events.
func (h *RecordingHook) Starts() []StageStartEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]StageStartEvent, len(h.starts))
	copy(out, h.starts)
	return out
}

// Items returns all recorded per-item events (including samples).
func (h *RecordingHook) Items() []ItemEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ItemEvent, len(h.items))
	copy(out, h.items)
	return out
}

// ItemsFor returns per-item events for the named stage.
func (h *RecordingHook) ItemsFor(stage string) []ItemEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []ItemEvent
	for _, e := range h.items {
		if e.Stage == stage {
			out = append(out, e)
		}
	}
	return out
}

// Errors returns only events where Err != nil.
func (h *RecordingHook) Errors() []ItemEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []ItemEvent
	for _, e := range h.items {
		if e.Err != nil {
			out = append(out, e)
		}
	}
	return out
}

// Dones returns all recorded stage-done events.
func (h *RecordingHook) Dones() []StageDoneEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]StageDoneEvent, len(h.dones))
	copy(out, h.dones)
	return out
}

// Drops returns all recorded overflow-drop events.
func (h *RecordingHook) Drops() []DropEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]DropEvent, len(h.drops))
	copy(out, h.drops)
	return out
}

// Restarts returns all recorded supervision-restart events.
func (h *RecordingHook) Restarts() []RestartEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RestartEvent, len(h.restarts))
	copy(out, h.restarts)
	return out
}

// Graph returns the pipeline graph snapshot received via OnGraph.
// Returns nil if OnGraph has not been called yet.
func (h *RecordingHook) Graph() []kitsune.GraphNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.graph == nil {
		return nil
	}
	out := make([]kitsune.GraphNode, len(h.graph))
	copy(out, h.graph)
	return out
}
