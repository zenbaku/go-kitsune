package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// RecordingHook
// ---------------------------------------------------------------------------

// RecordingHook records all lifecycle events for inspection in tests.
// It implements [Hook], [OverflowHook], [SupervisionHook], [SampleHook],
// [GraphHook], and [BufferHook].
type RecordingHook struct {
	mu      sync.Mutex
	starts  []string
	items   []itemRecord
	dones   []doneRecord
	drops   []any
	restarts []restartRecord
	samples []sampleRecord
	graph   []internal.GraphNode
}

type itemRecord struct {
	Stage string
	Dur   time.Duration
	Err   error
}

type doneRecord struct {
	Stage     string
	Processed int64
	Errors    int64
}

type restartRecord struct {
	Stage   string
	Attempt int
	Cause   error
}

type sampleRecord struct {
	Stage string
	Item  any
}

// OnStageStart records a stage start event.
func (h *RecordingHook) OnStageStart(_ context.Context, stage string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, stage)
}

// OnItem records a per-item event.
func (h *RecordingHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = append(h.items, itemRecord{Stage: stage, Dur: dur, Err: err})
}

// OnStageDone records a stage completion event.
func (h *RecordingHook) OnStageDone(_ context.Context, stage string, processed, errors int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dones = append(h.dones, doneRecord{Stage: stage, Processed: processed, Errors: errors})
}

// OnDrop records a dropped item event.
func (h *RecordingHook) OnDrop(_ context.Context, _ string, item any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drops = append(h.drops, item)
}

// OnStageRestart records a stage restart event.
func (h *RecordingHook) OnStageRestart(_ context.Context, stage string, attempt int, cause error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restarts = append(h.restarts, restartRecord{Stage: stage, Attempt: attempt, Cause: cause})
}

// OnItemSample records a sampled item.
func (h *RecordingHook) OnItemSample(_ context.Context, stage string, item any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, sampleRecord{Stage: stage, Item: item})
}

// OnGraph records the graph topology.
func (h *RecordingHook) OnGraph(nodes []internal.GraphNode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph = nodes
}

// OnBuffers is a no-op for the recording hook (buffer polling is not recorded).
func (h *RecordingHook) OnBuffers(_ func() []internal.BufferStatus) {}

// Starts returns all recorded stage start event names.
func (h *RecordingHook) Starts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.starts))
	copy(out, h.starts)
	return out
}

// Items returns all recorded per-item events.
func (h *RecordingHook) Items() []itemRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]itemRecord, len(h.items))
	copy(out, h.items)
	return out
}

// Errors returns per-item events that have a non-nil error.
func (h *RecordingHook) Errors() []itemRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []itemRecord
	for _, r := range h.items {
		if r.Err != nil {
			out = append(out, r)
		}
	}
	return out
}

// Dones returns all recorded stage completion events.
func (h *RecordingHook) Dones() []doneRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]doneRecord, len(h.dones))
	copy(out, h.dones)
	return out
}

// Drops returns all recorded dropped items.
func (h *RecordingHook) Drops() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]any, len(h.drops))
	copy(out, h.drops)
	return out
}

// Restarts returns all recorded stage restart events.
func (h *RecordingHook) Restarts() []restartRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]restartRecord, len(h.restarts))
	copy(out, h.restarts)
	return out
}

// Graph returns the last recorded graph topology.
func (h *RecordingHook) Graph() []internal.GraphNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]internal.GraphNode, len(h.graph))
	copy(out, h.graph)
	return out
}

// Samples returns all recorded item samples.
func (h *RecordingHook) Samples() []sampleRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]sampleRecord, len(h.samples))
	copy(out, h.samples)
	return out
}
