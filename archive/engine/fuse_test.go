package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// isFusibleNode
// ---------------------------------------------------------------------------

func TestIsFusibleNode(t *testing.T) {
	t.Run("map_default", func(t *testing.T) {
		n := &Node{Kind: Map, Concurrency: 1}
		if !isFusibleNode(n) {
			t.Fatal("expected fusible")
		}
	})
	t.Run("map_concurrency_gt_1", func(t *testing.T) {
		n := &Node{Kind: Map, Concurrency: 4}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: concurrency>1")
		}
	})
	t.Run("map_with_supervision", func(t *testing.T) {
		n := &Node{Kind: Map, Supervision: SupervisionPolicy{MaxRestarts: 1}}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: supervision")
		}
	})
	t.Run("map_explicit_default_handler_is_fusible", func(t *testing.T) {
		// Explicit DefaultHandler{} is the same as nil — both fuse.
		n := &Node{Kind: Map, ErrorHandler: DefaultHandler{}}
		if !isFusibleNode(n) {
			t.Fatal("expected fusible: explicit DefaultHandler is identical to nil")
		}
	})
	t.Run("map_with_skip_handler", func(t *testing.T) {
		type skipH struct{ DefaultHandler }
		n := &Node{Kind: Map, ErrorHandler: skipH{}}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: non-default error handler")
		}
	})
	t.Run("map_with_timeout", func(t *testing.T) {
		n := &Node{Kind: Map, Timeout: 1000}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: timeout")
		}
	})
	t.Run("map_with_cache_wrap", func(t *testing.T) {
		n := &Node{Kind: Map, CacheWrapFn: func(_ Cache, _ time.Duration, _ Codec) any { return nil }}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: cache wrap")
		}
	})
	t.Run("map_drop_newest", func(t *testing.T) {
		n := &Node{Kind: Map, Overflow: OverflowDropNewest}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: drop-newest overflow")
		}
	})
	t.Run("filter_default", func(t *testing.T) {
		n := &Node{Kind: Filter}
		if !isFusibleNode(n) {
			t.Fatal("expected fusible")
		}
	})
	t.Run("flatmap_not_fusible", func(t *testing.T) {
		// FlatMap is excluded from fusion to avoid per-item closure allocations.
		n := &Node{Kind: FlatMap}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: FlatMap excluded from fusion")
		}
	})
	t.Run("sink_default", func(t *testing.T) {
		n := &Node{Kind: Sink, Inputs: []InputRef{{0, 0}}}
		if !isFusibleNode(n) {
			t.Fatal("expected fusible")
		}
	})
	t.Run("sink_multi_input", func(t *testing.T) {
		n := &Node{Kind: Sink, Inputs: []InputRef{{0, 0}, {1, 0}}}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: multi-input sink")
		}
	})
	t.Run("source_not_fusible", func(t *testing.T) {
		n := &Node{Kind: Source}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: source")
		}
	})
	t.Run("batch_not_fusible", func(t *testing.T) {
		n := &Node{Kind: Batch}
		if isFusibleNode(n) {
			t.Fatal("expected non-fusible: batch")
		}
	})
}

// ---------------------------------------------------------------------------
// detectFusionChains
// ---------------------------------------------------------------------------

// buildLinearGraph creates: Source(0) → Map(1) → Filter(2) → Sink(3)
func buildLinearGraph() *Graph {
	g := New()
	g.AddNode(&Node{Kind: Source})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{0, 0}},
		Fn: func(ctx context.Context, v any) (any, error) { return v.(int) * 2, nil }})
	g.AddNode(&Node{Kind: Filter, Inputs: []InputRef{{1, 0}},
		Fn: func(v any) bool { return v.(int) > 0 }})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{2, 0}},
		Fn: func(ctx context.Context, v any) error { return nil }})
	return g
}

func TestDetectFusionChains_NoopHookRequired(t *testing.T) {
	g := buildLinearGraph()
	chans := CreateChannels(g)

	// Non-NoopHook must return nil.
	chains := detectFusionChains(g, chans, &testHook{})
	if chains != nil {
		t.Fatal("expected nil chains with non-NoopHook")
	}

	// NoopHook must detect chains.
	chains = detectFusionChains(g, chans, NoopHook{})
	if len(chains) == 0 {
		t.Fatal("expected fusion chains with NoopHook")
	}
}

func TestDetectFusionChains_Linear(t *testing.T) {
	// Graph: Source(0) → Map(1) → Filter(2) → Sink(3)
	// Expected: one chain [Map, Filter, Sink] with head=1
	g := buildLinearGraph()
	chans := CreateChannels(g)
	chains := detectFusionChains(g, chans, NoopHook{})

	if len(chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(chains))
	}
	chain, ok := chains[1] // head is Map (node 1)
	if !ok {
		t.Fatal("expected chain head at node 1 (Map)")
	}
	if len(chain.steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(chain.steps))
	}
	if chain.steps[0].kind != Map {
		t.Errorf("step 0: want Map, got %d", chain.steps[0].kind)
	}
	if chain.steps[1].kind != Filter {
		t.Errorf("step 1: want Filter, got %d", chain.steps[1].kind)
	}
	if chain.steps[2].kind != Sink {
		t.Errorf("step 2: want Sink, got %d", chain.steps[2].kind)
	}
	if !chain.isSinkTail {
		t.Error("expected isSinkTail")
	}
	if chain.outCh != nil {
		t.Error("expected nil outCh for sink-tail chain")
	}
	if chain.inCh == nil {
		t.Error("expected non-nil inCh")
	}
}

func TestDetectFusionChains_BrokenByNonFusible(t *testing.T) {
	// Source(0) → Map(1) → Batch(2) → Map(3) → Sink(4)
	// Batch is not fusible, so chains are [Map(1)] (< 2 nodes, skipped)
	// and [Map(3), Sink(4)].
	g := New()
	g.AddNode(&Node{Kind: Source})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{0, 0}},
		Fn: func(ctx context.Context, v any) (any, error) { return v, nil }})
	g.AddNode(&Node{Kind: Batch, Inputs: []InputRef{{1, 0}}, BatchSize: 10,
		BatchConvert: func(b []any) any { return b }})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{2, 0}},
		Fn: func(ctx context.Context, v any) (any, error) { return v, nil }})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{3, 0}},
		Fn: func(ctx context.Context, v any) error { return nil }})
	chans := CreateChannels(g)
	chains := detectFusionChains(g, chans, NoopHook{})

	// Only the [Map(3), Sink(4)] chain should be detected.
	if len(chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(chains))
	}
	if _, ok := chains[3]; !ok {
		t.Error("expected chain head at node 3")
	}
}

func TestDetectFusionChains_FanOut(t *testing.T) {
	// Source(0) → Map(1) → Sink(2)
	//                    ↘ Sink(3)   (two consumers of Map's output — fan-out)
	// This is structurally invalid per graph validation, but we test that
	// detectFusionChains handles it gracefully by not fusing at Map(1).
	g := New()
	g.AddNode(&Node{Kind: Source})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{0, 0}},
		Fn: func(ctx context.Context, v any) (any, error) { return v, nil }})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{1, 0}},
		Fn: func(ctx context.Context, v any) error { return nil }})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{1, 0}},
		Fn: func(ctx context.Context, v any) error { return nil }})
	chans := CreateChannels(g)
	chains := detectFusionChains(g, chans, NoopHook{})
	// Map(1)'s output has two consumers → outputConsumer[{1,0}] == -1
	// So no chain starting at Map(1) should be built.
	if len(chains) != 0 {
		t.Fatalf("expected no chains with fan-out, got %d", len(chains))
	}
}

func TestDetectFusionChains_Supervision(t *testing.T) {
	// Map with supervision is not fusible → no chain.
	g := New()
	g.AddNode(&Node{Kind: Source})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{0, 0}},
		Supervision: SupervisionPolicy{MaxRestarts: 1},
		Fn:          func(ctx context.Context, v any) (any, error) { return v, nil }})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{1, 0}},
		Fn: func(ctx context.Context, v any) error { return nil }})
	chans := CreateChannels(g)
	chains := detectFusionChains(g, chans, NoopHook{})
	if len(chains) != 0 {
		t.Fatalf("expected no chains for supervised map, got %d", len(chains))
	}
}

// ---------------------------------------------------------------------------
// runFusedLinear correctness
// ---------------------------------------------------------------------------

func TestRunFusedLinear_MapFilter(t *testing.T) {
	steps := []fusedStep{
		{kind: Map, name: "double", mapFn: func(_ context.Context, v any) (any, error) {
			return v.(int) * 2, nil
		}},
		{kind: Filter, name: "pos", filterFn: func(v any) bool { return v.(int) > 4 }},
	}
	inCh := make(chan any, 10)
	outCh := make(chan any, 10)

	for i := 1; i <= 5; i++ {
		inCh <- i
	}
	close(inCh)

	if err := runFusedLinear(context.Background(), steps, inCh, outCh); err != nil {
		t.Fatal(err)
	}
	close(outCh)

	var got []int
	for v := range outCh {
		got = append(got, v.(int))
	}
	// Items: 1*2=2 (filtered), 2*2=4 (filtered), 3*2=6, 4*2=8, 5*2=10
	want := []int{6, 8, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d] got %d, want %d", i, got[i], v)
		}
	}
}

func TestRunFusedLinear_MapError(t *testing.T) {
	sentinel := errors.New("map error")
	steps := []fusedStep{
		{kind: Map, name: "fail", mapFn: func(_ context.Context, v any) (any, error) {
			if v.(int) == 3 {
				return nil, sentinel
			}
			return v, nil
		}},
	}
	inCh := make(chan any, 10)
	outCh := make(chan any, 10)
	for i := 1; i <= 5; i++ {
		inCh <- i
	}
	close(inCh)

	err := runFusedLinear(context.Background(), steps, inCh, outCh)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestRunFusedLinear_SinkTail(t *testing.T) {
	var collected []int
	steps := []fusedStep{
		{kind: Map, name: "double", mapFn: func(_ context.Context, v any) (any, error) {
			return v.(int) * 2, nil
		}},
		{kind: Sink, name: "collect", sinkFn: func(_ context.Context, v any) error {
			collected = append(collected, v.(int))
			return nil
		}},
	}
	inCh := make(chan any, 10)
	for i := 1; i <= 3; i++ {
		inCh <- i
	}
	close(inCh)

	if err := runFusedLinear(context.Background(), steps, inCh, nil); err != nil {
		t.Fatal(err)
	}
	want := []int{2, 4, 6}
	if len(collected) != len(want) {
		t.Fatalf("got %v, want %v", collected, want)
	}
}

// ---------------------------------------------------------------------------
// End-to-end integration: verify fusion produces identical results
// ---------------------------------------------------------------------------

// testHook is a non-NoopHook that disables fusion (used to get a reference result).
type testHook struct{ NoopHook }

func (testHook) OnStageStart(ctx context.Context, stage string) {}

// runFusionPipeline runs a Source→Map→Filter→Sink pipeline through engine.Run,
// with the given hook. Fusion is active only when hook is NoopHook.
func runFusionPipeline(t *testing.T, hook Hook) []int {
	t.Helper()
	g := New()

	input := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	g.AddNode(&Node{Kind: Source, Fn: func(ctx context.Context, yield func(any) bool) error {
		for _, v := range input {
			if !yield(v) {
				return nil
			}
		}
		return nil
	}})
	g.AddNode(&Node{Kind: Map, Inputs: []InputRef{{0, 0}},
		Fn: func(_ context.Context, v any) (any, error) { return v.(int) * 3, nil }})
	g.AddNode(&Node{Kind: Filter, Inputs: []InputRef{{1, 0}},
		Fn: func(v any) bool { return v.(int)%2 != 0 }}) // keep odd
	var results []int
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{2, 0}},
		Fn: func(_ context.Context, v any) error {
			results = append(results, v.(int))
			return nil
		}})

	if err := Run(context.Background(), g, RunConfig{Hook: hook}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return results
}

func TestFusion_IdenticalResults(t *testing.T) {
	withFusion := runFusionPipeline(t, NoopHook{})
	withoutFusion := runFusionPipeline(t, testHook{})

	if len(withFusion) != len(withoutFusion) {
		t.Fatalf("length mismatch: fusion=%d, no-fusion=%d", len(withFusion), len(withoutFusion))
	}
	for i, v := range withFusion {
		if v != withoutFusion[i] {
			t.Errorf("[%d] fusion=%d, no-fusion=%d", i, v, withoutFusion[i])
		}
	}
}
