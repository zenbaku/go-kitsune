package kitsune

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// MetricsHook — lock-free per-stage counters
// ---------------------------------------------------------------------------

// StageMetrics holds a point-in-time snapshot of one stage's counters.
type StageMetrics struct {
	Stage     string        `json:"stage"`
	Processed int64         `json:"processed"`
	Errors    int64         `json:"errors"`
	Dropped   int64         `json:"dropped"`   // overflow drops
	Restarts  int64         `json:"restarts"`  // supervision restarts
	TotalTime time.Duration `json:"total_time"` // sum of per-item processing durations
}

// AvgLatency returns the mean per-item processing time. Returns 0 if no items
// have been processed yet.
func (s StageMetrics) AvgLatency() time.Duration {
	if s.Processed == 0 {
		return 0
	}
	return s.TotalTime / time.Duration(s.Processed)
}

type stageCounters struct {
	processed atomic.Int64
	errors    atomic.Int64
	dropped   atomic.Int64
	restarts  atomic.Int64
	totalNs   atomic.Int64 // total processing nanoseconds
}

func (c *stageCounters) snapshot(name string) StageMetrics {
	ns := c.totalNs.Load()
	proc := c.processed.Load()
	return StageMetrics{
		Stage:     name,
		Processed: proc,
		Errors:    c.errors.Load(),
		Dropped:   c.dropped.Load(),
		Restarts:  c.restarts.Load(),
		TotalTime: time.Duration(ns),
	}
}

// MetricsSnapshot is a point-in-time capture of all stage metrics.
type MetricsSnapshot struct {
	Stages []StageMetrics `json:"stages"`
}

// JSON returns the snapshot serialised as indented JSON.
func (s MetricsSnapshot) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// MetricsHook collects per-stage counters across the full pipeline run.
// It implements [Hook], [OverflowHook], and [SupervisionHook] — wire it in with
// [WithHook] and call [MetricsHook.Snapshot] after Run completes (or during a
// run to observe live values).
//
//	m := kitsune.NewMetricsHook()
//	runner.Run(ctx, kitsune.WithHook(m))
//	snap := m.Snapshot()
type MetricsHook struct {
	mu       sync.RWMutex
	counters map[string]*stageCounters
}

// NewMetricsHook returns a ready-to-use [MetricsHook].
func NewMetricsHook() *MetricsHook {
	return &MetricsHook{counters: make(map[string]*stageCounters)}
}

func (m *MetricsHook) stage(name string) *stageCounters {
	m.mu.RLock()
	c, ok := m.counters[name]
	m.mu.RUnlock()
	if ok {
		return c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok = m.counters[name]; ok {
		return c
	}
	c = &stageCounters{}
	m.counters[name] = c
	return c
}

// Stage returns the live counters for a single stage by name.
// Returns a zero-value snapshot if the stage has not been seen yet.
func (m *MetricsHook) Stage(name string) StageMetrics {
	m.mu.RLock()
	c, ok := m.counters[name]
	m.mu.RUnlock()
	if !ok {
		return StageMetrics{Stage: name}
	}
	return c.snapshot(name)
}

// Snapshot returns a point-in-time capture of all stage metrics.
func (m *MetricsHook) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	names := make([]string, 0, len(m.counters))
	for name := range m.counters {
		names = append(names, name)
	}
	m.mu.RUnlock()

	snap := MetricsSnapshot{Stages: make([]StageMetrics, 0, len(names))}
	for _, name := range names {
		m.mu.RLock()
		c := m.counters[name]
		m.mu.RUnlock()
		snap.Stages = append(snap.Stages, c.snapshot(name))
	}
	return snap
}

// Reset clears all counters, returning the hook to its initial zero state.
func (m *MetricsHook) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = make(map[string]*stageCounters)
}

// ---------------------------------------------------------------------------
// Hook interface implementation
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnStageStart(_ context.Context, stage string) {
	m.stage(stage) // ensure entry exists
}

func (m *MetricsHook) OnItem(_ context.Context, stage string, dur time.Duration, err error) {
	c := m.stage(stage)
	if err != nil {
		c.errors.Add(1)
	} else {
		c.processed.Add(1)
		c.totalNs.Add(int64(dur))
	}
}

func (m *MetricsHook) OnStageDone(_ context.Context, _ string, _, _ int64) {
	// Totals are maintained incrementally via OnItem; nothing extra needed here.
}

// ---------------------------------------------------------------------------
// OverflowHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnDrop(_ context.Context, stage string, _ any) {
	m.stage(stage).dropped.Add(1)
}

// ---------------------------------------------------------------------------
// SupervisionHook
// ---------------------------------------------------------------------------

func (m *MetricsHook) OnStageRestart(_ context.Context, stage string, _ int, _ error) {
	m.stage(stage).restarts.Add(1)
}
