package inspector

import (
	"context"
	"sync"
	"time"

	kithooks "github.com/zenbaku/go-kitsune/hooks"
)

// LogEntry is one inspector event log entry.
// It is exported so InspectorStore implementations can inspect or persist entries directly.
type LogEntry struct {
	TS    int64  `json:"ts"`
	Type  string `json:"type"`
	Stage string `json:"stage,omitempty"`
	Msg   string `json:"msg,omitempty"`
}

// PersistedStage is a point-in-time snapshot of a stage's counters, safe to
// marshal and restore across Inspector restarts.
type PersistedStage struct {
	Items    int64  `json:"items"`
	Errors   int64  `json:"errors"`
	Drops    int64  `json:"drops"`
	Restarts int64  `json:"restarts"`
	TotalNs  int64  `json:"total_ns"`
	Status   string `json:"status"` // "pending" | "running" | "done"
}

// InspectorStore persists Inspector state between restarts, enabling post-mortem
// analysis and cumulative metric history across pipeline restart loops.
//
// Use [NewMemoryInspectorStore] for an in-process implementation. For
// cross-restart persistence implement this interface over any external backend
// (Redis, SQLite, etc.).
//
// All methods must be safe for concurrent use.
type InspectorStore interface {
	// SaveGraph persists the pipeline graph topology.
	SaveGraph(ctx context.Context, nodes []kithooks.GraphNode) error
	// LoadGraph returns the previously saved graph, or (nil, nil) if none exists.
	LoadGraph(ctx context.Context) ([]kithooks.GraphNode, error)

	// SaveStages persists stage metric snapshots and their ordered names.
	SaveStages(ctx context.Context, order []string, stages map[string]PersistedStage) error
	// LoadStages returns the last saved stage order and snapshots, or (nil, nil, nil) if none.
	LoadStages(ctx context.Context) (order []string, stages map[string]PersistedStage, err error)

	// SaveLog persists the log ring buffer.
	SaveLog(ctx context.Context, entries []LogEntry) error
	// LoadLog returns the last saved log entries, or (nil, nil) if none.
	LoadLog(ctx context.Context) ([]LogEntry, error)

	// SaveSummary persists the most recent RunSummary snapshot. Implementations
	// overwrite any previous snapshot. Pass nil to clear.
	SaveSummary(ctx context.Context, summary *SummarySnapshot) error

	// LoadSummary returns the persisted summary snapshot, or (nil, nil) if no
	// summary has been saved yet. Errors are returned only for I/O failures;
	// "no summary saved" is not an error.
	LoadSummary(ctx context.Context) (*SummarySnapshot, error)
}

// NewMemoryInspectorStore returns an InspectorStore that holds state in process
// memory. State survives Inspector restarts within the same process but is lost
// when the process exits.
//
// logTTL controls how long log entries are retained. Pass 0 to keep all entries
// up to the log capacity (200). Pass a positive duration to evict entries older
// than logTTL on each save; this bounds memory on long-running processes that
// restart frequently.
func NewMemoryInspectorStore(logTTL time.Duration) InspectorStore {
	return &memoryInspectorStore{logTTL: logTTL}
}

type memoryInspectorStore struct {
	mu      sync.RWMutex
	graph   []kithooks.GraphNode
	order   []string
	stages  map[string]PersistedStage
	log     []LogEntry
	logTTL  time.Duration
	summary *SummarySnapshot
}

func (m *memoryInspectorStore) SaveGraph(_ context.Context, nodes []kithooks.GraphNode) error {
	cp := make([]kithooks.GraphNode, len(nodes))
	copy(cp, nodes)
	m.mu.Lock()
	m.graph = cp
	m.mu.Unlock()
	return nil
}

func (m *memoryInspectorStore) LoadGraph(_ context.Context) ([]kithooks.GraphNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.graph == nil {
		return nil, nil
	}
	cp := make([]kithooks.GraphNode, len(m.graph))
	copy(cp, m.graph)
	return cp, nil
}

func (m *memoryInspectorStore) SaveStages(_ context.Context, order []string, stages map[string]PersistedStage) error {
	orderCp := make([]string, len(order))
	copy(orderCp, order)
	stagesCp := make(map[string]PersistedStage, len(stages))
	for k, v := range stages {
		stagesCp[k] = v
	}
	m.mu.Lock()
	m.order = orderCp
	m.stages = stagesCp
	m.mu.Unlock()
	return nil
}

func (m *memoryInspectorStore) LoadStages(_ context.Context) ([]string, map[string]PersistedStage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stages == nil {
		return nil, nil, nil
	}
	orderCp := make([]string, len(m.order))
	copy(orderCp, m.order)
	stagesCp := make(map[string]PersistedStage, len(m.stages))
	for k, v := range m.stages {
		stagesCp[k] = v
	}
	return orderCp, stagesCp, nil
}

func (m *memoryInspectorStore) SaveLog(_ context.Context, entries []LogEntry) error {
	filtered := entries
	if m.logTTL > 0 {
		cutoff := time.Now().Add(-m.logTTL).UnixMilli()
		filtered = make([]LogEntry, 0, len(entries))
		for _, e := range entries {
			if e.TS >= cutoff {
				filtered = append(filtered, e)
			}
		}
	}
	cp := make([]LogEntry, len(filtered))
	copy(cp, filtered)
	m.mu.Lock()
	m.log = cp
	m.mu.Unlock()
	return nil
}

func (m *memoryInspectorStore) LoadLog(_ context.Context) ([]LogEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.log == nil {
		return nil, nil
	}
	cp := make([]LogEntry, len(m.log))
	copy(cp, m.log)
	return cp, nil
}

func (m *memoryInspectorStore) SaveSummary(_ context.Context, s *SummarySnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s == nil {
		m.summary = nil
		return nil
	}
	cp := *s
	m.summary = &cp
	return nil
}

func (m *memoryInspectorStore) LoadSummary(_ context.Context) (*SummarySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.summary == nil {
		return nil, nil
	}
	cp := *m.summary
	return &cp, nil
}
