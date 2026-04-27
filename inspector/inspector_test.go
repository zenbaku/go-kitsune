package inspector_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
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

// TestInspector_OnRunComplete verifies that the inspector receives the run
// summary when the runner completes, broadcasts a "summary" SSE event, and
// includes the summary in /state for browser refreshes.
func TestInspector_OnRunComplete(t *testing.T) {
	insp := inspector.New()
	defer insp.Close()

	src := kitsune.FromSlice([]int{1, 2, 3})
	runner := src.ForEach(func(_ context.Context, _ int) error { return nil })

	summary, err := runner.Run(context.Background(), kitsune.WithHook(insp))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != kitsune.RunSuccess {
		t.Fatalf("Outcome=%v, want RunSuccess", summary.Outcome)
	}

	// Allow the broadcast goroutine to deliver the SSE event before we read
	// /state. The handleState handler reads i.lastRunSummary directly under
	// i.mu, so the value is visible immediately after Run returns; this
	// sleep is a safety margin for goroutine scheduling under -race.
	time.Sleep(50 * time.Millisecond)

	// /state should now include the summary.
	res, err := http.Get(insp.URL() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Summary *struct {
			Outcome     int    `json:"outcome"`
			OutcomeName string `json:"outcomeName"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary == nil {
		t.Fatalf("expected summary in /state response; got body: %s", body)
	}
	if got.Summary.OutcomeName != "RunSuccess" {
		t.Errorf("OutcomeName=%q, want RunSuccess", got.Summary.OutcomeName)
	}
}

// TestInspector_OnRunComplete_FinalizerErrors verifies that finalizer
// errors flow through the summary the inspector sees and are surfaced as
// strings in the SSE/state JSON shape.
func TestInspector_OnRunComplete_FinalizerErrors(t *testing.T) {
	insp := inspector.New()
	defer insp.Close()

	sentinel := errors.New("finalizer-failed")
	src := kitsune.FromSlice([]int{1})
	runner := src.ForEach(func(_ context.Context, _ int) error { return nil }).
		WithFinalizer(func(_ context.Context, _ kitsune.RunSummary) error {
			return sentinel
		})

	summary, err := runner.Run(context.Background(), kitsune.WithHook(insp))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.FinalizerErrs) != 1 || !errors.Is(summary.FinalizerErrs[0], sentinel) {
		t.Fatalf("FinalizerErrs=%v, want [%v]", summary.FinalizerErrs, sentinel)
	}

	time.Sleep(50 * time.Millisecond)

	res, err := http.Get(insp.URL() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Summary *struct {
			FinalizerErrs []string `json:"finalizerErrs"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary == nil || len(got.Summary.FinalizerErrs) != 1 ||
		got.Summary.FinalizerErrs[0] != "finalizer-failed" {
		t.Errorf("FinalizerErrs in /state = %+v, want [\"finalizer-failed\"]", got.Summary)
	}
}

// TestInspector_SegmentReplay_Visible verifies that when a Segment replays
// from a DevStore snapshot, it appears in /state as a stage with kind
// "segment-replay", and the run's graph payload includes the matching node.
func TestInspector_SegmentReplay_Visible(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	// Seed a snapshot directly.
	raws := []json.RawMessage{
		json.RawMessage(`1`),
		json.RawMessage(`2`),
		json.RawMessage(`3`),
	}
	if err := store.Save(context.Background(), "ingest", raws); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	insp := inspector.New()
	defer insp.Close()

	src := kitsune.FromSlice([]int{})
	seg := kitsune.NewSegment("ingest", kitsune.Stage[int, int](
		func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
			return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v, nil })
		}))

	got := []int{}
	_, err := seg.Apply(src).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("downstream got %v, want [1 2 3]", got)
	}

	state := getState(t, insp.URL())

	var node *kithooks.GraphNode
	for i := range state.Graph {
		if state.Graph[i].Kind == "segment-replay" {
			node = &state.Graph[i]
			break
		}
	}
	if node == nil {
		t.Fatalf("no segment-replay node in /state graph; got: %+v", state.Graph)
	}
	if node.Name != "ingest" || node.SegmentName != "ingest" {
		t.Errorf("graph node Name=%q SegmentName=%q, want both %q",
			node.Name, node.SegmentName, "ingest")
	}

	raw, ok := state.Stats["ingest"]
	if !ok {
		t.Fatalf("no Stats[\"ingest\"] in /state; got keys: %v", keys(state.Stats))
	}
	var snap struct {
		Items  int64  `json:"items"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode stage snapshot: %v", err)
	}
	if snap.Items != 3 {
		t.Errorf("Items=%d, want 3", snap.Items)
	}
	if snap.Status != "done" {
		t.Errorf("Status=%q, want \"done\"", snap.Status)
	}
}

// keys is a small helper for diagnostic output in test failure messages.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestInspector_SegmentReplay_CaptureThenReplay verifies an end-to-end
// roundtrip: run 1 captures (inner stages fire normal events), run 2
// replays (synthetic segment-replay stage replaces the inner-stage events).
func TestInspector_SegmentReplay_CaptureThenReplay(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	stage := kitsune.Stage[int, int](func(p *kitsune.Pipeline[int]) *kitsune.Pipeline[int] {
		return kitsune.Map(p, func(_ context.Context, v int) (int, error) { return v + 100, nil },
			kitsune.WithName("inner-map"))
	})

	// --- Run 1: capture ---
	insp1 := inspector.New()
	src1 := kitsune.FromSlice([]int{1, 2})
	seg1 := kitsune.NewSegment("roundtrip", stage)
	out1 := []int{}
	if _, err := seg1.Apply(src1).ForEach(func(_ context.Context, v int) error {
		out1 = append(out1, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp1)); err != nil {
		t.Fatalf("capture run: %v", err)
	}
	if !reflect.DeepEqual(out1, []int{101, 102}) {
		t.Fatalf("capture out=%v, want [101 102]", out1)
	}

	state1 := getState(t, insp1.URL())
	insp1.Close()

	// Capture run: graph should contain the inner Map (stage name "inner-map"),
	// no segment-replay node.
	var sawInner1, sawReplay1 bool
	for _, n := range state1.Graph {
		if n.Name == "inner-map" {
			sawInner1 = true
		}
		if n.Kind == "segment-replay" {
			sawReplay1 = true
		}
	}
	if !sawInner1 {
		t.Errorf("capture run: graph missing inner-map node; got: %+v", state1.Graph)
	}
	if sawReplay1 {
		t.Errorf("capture run: graph unexpectedly has segment-replay node")
	}

	// --- Run 2: replay ---
	insp2 := inspector.New()
	defer insp2.Close()
	src2 := kitsune.FromSlice([]int{})
	seg2 := kitsune.NewSegment("roundtrip", stage)
	out2 := []int{}
	if _, err := seg2.Apply(src2).ForEach(func(_ context.Context, v int) error {
		out2 = append(out2, v)
		return nil
	}).Run(context.Background(),
		kitsune.WithDevStore(store), kitsune.WithHook(insp2)); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if !reflect.DeepEqual(out2, []int{101, 102}) {
		t.Fatalf("replay out=%v, want [101 102]", out2)
	}

	state2 := getState(t, insp2.URL())

	// Replay run: graph should contain segment-replay node with name
	// "roundtrip"; no inner-map node.
	var sawInner2, sawReplay2 bool
	for _, n := range state2.Graph {
		if n.Name == "inner-map" {
			sawInner2 = true
		}
		if n.Kind == "segment-replay" && n.Name == "roundtrip" {
			sawReplay2 = true
		}
	}
	if sawInner2 {
		t.Errorf("replay run: graph unexpectedly has inner-map node")
	}
	if !sawReplay2 {
		t.Errorf("replay run: graph missing segment-replay node; got: %+v", state2.Graph)
	}
}
