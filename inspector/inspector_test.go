package inspector_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	kithooks "github.com/zenbaku/go-kitsune/hooks"
	"github.com/zenbaku/go-kitsune/inspector"
)

// stateResponse matches the JSON returned by GET /state.
type stateResponse struct {
	Graph []kithooks.GraphNode       `json:"graph"`
	Stats map[string]json.RawMessage `json:"stats"`
}

func getState(t *testing.T, url string) stateResponse {
	t.Helper()
	resp, err := http.Get(url + "/state")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	defer resp.Body.Close()
	var s stateResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatalf("decode /state: %v", err)
	}
	return s
}

func TestInspectorWithoutStore(t *testing.T) {
	// No options: original behavior, no panic.
	insp := inspector.New()
	defer insp.Close()
	insp.OnStageStart(context.Background(), "s")
	insp.OnItem(context.Background(), "s", time.Millisecond, nil)
}

func TestMemoryInspectorStoreEmpty(t *testing.T) {
	store := inspector.NewMemoryInspectorStore(0)
	ctx := context.Background()

	graph, err := store.LoadGraph(ctx)
	if err != nil || graph != nil {
		t.Errorf("expected nil graph on empty store, got %v / %v", graph, err)
	}

	order, stages, err := store.LoadStages(ctx)
	if err != nil || stages != nil || order != nil {
		t.Errorf("expected nil stages on empty store")
	}

	entries, err := store.LoadLog(ctx)
	if err != nil || entries != nil {
		t.Errorf("expected nil log on empty store")
	}
}

func TestMemoryInspectorStoreRoundTrip(t *testing.T) {
	store := inspector.NewMemoryInspectorStore(0)
	ctx := context.Background()

	nodes := []kithooks.GraphNode{{ID: 1, Name: "parse", Kind: "Map"}}
	if err := store.SaveGraph(ctx, nodes); err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	loaded, err := store.LoadGraph(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Name != "parse" {
		t.Errorf("graph round-trip failed: %v / %v", loaded, err)
	}

	// Mutations to original slice must not affect stored copy.
	nodes[0].Name = "mutated"
	loaded2, _ := store.LoadGraph(ctx)
	if loaded2[0].Name != "parse" {
		t.Errorf("store must deep-copy graph on save")
	}

	order := []string{"s1", "s2"}
	stages := map[string]inspector.PersistedStage{
		"s1": {Items: 10, Errors: 1, Status: "done"},
		"s2": {Items: 5, Status: "running"},
	}
	if err := store.SaveStages(ctx, order, stages); err != nil {
		t.Fatalf("SaveStages: %v", err)
	}
	gotOrder, gotStages, err := store.LoadStages(ctx)
	if err != nil {
		t.Fatalf("LoadStages: %v", err)
	}
	if len(gotOrder) != 2 || gotOrder[0] != "s1" || gotOrder[1] != "s2" {
		t.Errorf("order not preserved: %v", gotOrder)
	}
	if gotStages["s1"].Items != 10 || gotStages["s1"].Errors != 1 {
		t.Errorf("s1 not restored correctly: %+v", gotStages["s1"])
	}
}

func TestMemoryInspectorStoreLogTTL(t *testing.T) {
	store := inspector.NewMemoryInspectorStore(time.Hour)
	ctx := context.Background()

	old := inspector.LogEntry{TS: time.Now().Add(-2 * time.Hour).UnixMilli(), Type: "start", Stage: "s1"}
	recent := inspector.LogEntry{TS: time.Now().UnixMilli(), Type: "done", Stage: "s1"}

	if err := store.SaveLog(ctx, []inspector.LogEntry{old, recent}); err != nil {
		t.Fatalf("SaveLog: %v", err)
	}
	loaded, err := store.LoadLog(ctx)
	if err != nil {
		t.Fatalf("LoadLog: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Type != "done" {
		t.Errorf("expected only recent log entry after TTL filtering, got %v", loaded)
	}
}

func TestInspectorStoreRoundTrip(t *testing.T) {
	store := inspector.NewMemoryInspectorStore(0)

	// First inspector: record some events and close (which triggers a final save).
	insp, err := inspector.NewAt("localhost:0", inspector.WithStore(store))
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	nodes := []kithooks.GraphNode{{ID: 1, Name: "parse", Kind: "Map"}}
	insp.OnGraph(nodes)
	insp.OnStageStart(context.Background(), "parse")
	insp.OnItem(context.Background(), "parse", 10*time.Millisecond, nil)
	insp.OnItem(context.Background(), "parse", 5*time.Millisecond, nil)
	insp.OnDrop(context.Background(), "parse", "x")
	insp.Close()

	// Second inspector restores from the same store.
	insp2, err := inspector.NewAt("localhost:0", inspector.WithStore(store))
	if err != nil {
		t.Fatalf("NewAt (2nd): %v", err)
	}
	defer insp2.Close()

	state := getState(t, insp2.URL())

	if len(state.Graph) != 1 || state.Graph[0].Name != "parse" {
		t.Errorf("graph not restored: %+v", state.Graph)
	}

	raw, ok := state.Stats["parse"]
	if !ok {
		t.Fatalf("stage 'parse' not present in restored stats")
	}
	var snap struct {
		Items int64 `json:"items"`
		Drops int64 `json:"drops"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal stage snapshot: %v", err)
	}
	if snap.Items != 2 {
		t.Errorf("expected 2 items restored, got %d", snap.Items)
	}
	if snap.Drops != 1 {
		t.Errorf("expected 1 drop restored, got %d", snap.Drops)
	}
}

func TestInspectorAccumulatesAcrossRestart(t *testing.T) {
	store := inspector.NewMemoryInspectorStore(0)

	run := func(items int) {
		insp, err := inspector.NewAt("localhost:0", inspector.WithStore(store))
		if err != nil {
			t.Fatalf("NewAt: %v", err)
		}
		insp.OnStageStart(context.Background(), "stage")
		for range items {
			insp.OnItem(context.Background(), "stage", time.Millisecond, nil)
		}
		insp.Close()
	}

	run(10)
	run(15)

	// Third inspector should see 25 total items.
	insp3, err := inspector.NewAt("localhost:0", inspector.WithStore(store))
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	defer insp3.Close()

	state := getState(t, insp3.URL())
	raw, ok := state.Stats["stage"]
	if !ok {
		t.Fatalf("stage not present in restored stats")
	}
	var snap struct {
		Items int64 `json:"items"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.Items != 25 {
		t.Errorf("expected 25 accumulated items, got %d", snap.Items)
	}
}

func TestInspectorStoreErrorIsNonFatal(t *testing.T) {
	store := &errorStore{}
	insp, err := inspector.NewAt("localhost:0", inspector.WithStore(store))
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	insp.OnStageStart(context.Background(), "s")
	insp.OnItem(context.Background(), "s", time.Millisecond, nil)
	insp.Close() // must not panic
}

// errorStore is an InspectorStore that always returns errors.
type errorStore struct{}

func (e *errorStore) SaveGraph(_ context.Context, _ []kithooks.GraphNode) error {
	return errAlwaysFail
}
func (e *errorStore) LoadGraph(_ context.Context) ([]kithooks.GraphNode, error) {
	return nil, errAlwaysFail
}
func (e *errorStore) SaveStages(_ context.Context, _ []string, _ map[string]inspector.PersistedStage) error {
	return errAlwaysFail
}
func (e *errorStore) LoadStages(_ context.Context) ([]string, map[string]inspector.PersistedStage, error) {
	return nil, nil, errAlwaysFail
}
func (e *errorStore) SaveLog(_ context.Context, _ []inspector.LogEntry) error { return errAlwaysFail }
func (e *errorStore) LoadLog(_ context.Context) ([]inspector.LogEntry, error) {
	return nil, errAlwaysFail
}
func (e *errorStore) SaveSummary(_ context.Context, _ *inspector.SummarySnapshot) error {
	return errAlwaysFail
}
func (e *errorStore) LoadSummary(_ context.Context) (*inspector.SummarySnapshot, error) {
	return nil, errAlwaysFail
}

var errAlwaysFail = errorString("store: always fails")

type errorString string

func (e errorString) Error() string { return string(e) }
