package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sourceFn builds a Source Fn that yields items from a slice.
func sourceFn(items ...any) func(context.Context, func(any) bool) error {
	return func(_ context.Context, yield func(any) bool) error {
		for _, item := range items {
			if !yield(item) {
				return nil
			}
		}
		return nil
	}
}

// collectSink adds a Sink to g that appends every item to *out.
// Returns the node ID. Concurrency is always 1 to preserve order.
func collectSink(g *Graph, upstream int, out *[]any) int {
	var mu sync.Mutex
	return g.AddNode(&Node{
		Kind:         Sink,
		Fn:           func(_ context.Context, item any) error { mu.Lock(); *out = append(*out, item); mu.Unlock(); return nil },
		Inputs:       []InputRef{{upstream, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
}

func run(g *Graph) error {
	return Run(context.Background(), g, RunConfig{})
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestValidate_EmptyGraph(t *testing.T) {
	err := Validate(New())
	if err == nil {
		t.Fatal("expected error for empty graph")
	}
}

func TestValidate_NoSink(t *testing.T) {
	g := New()
	g.AddNode(&Node{
		Kind:         Source,
		Fn:           sourceFn(1),
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	if err := Validate(g); err == nil {
		t.Fatal("expected error for graph with no sink")
	}
}

func TestValidate_DuplicateConsumer(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	// Two nodes consuming the same (src, port 0).
	var out []any
	g.AddNode(&Node{Kind: Sink, Fn: func(_ context.Context, item any) error { out = append(out, item); return nil }, Inputs: []InputRef{{src, 0}}, Concurrency: 1, Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: Sink, Fn: func(_ context.Context, item any) error { out = append(out, item); return nil }, Inputs: []InputRef{{src, 0}}, Concurrency: 1, Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	if err := Validate(g); err == nil {
		t.Fatal("expected error for duplicate consumer")
	}
}

func TestValidate_ValidGraph(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	var out []any
	collectSink(g, src, &out)
	if err := Validate(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateChannels
// ---------------------------------------------------------------------------

func TestCreateChannels_SingleOutput(t *testing.T) {
	g := New()
	g.AddNode(&Node{Kind: Source, Buffer: 8, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{0, 0}}, Buffer: 8, ErrorHandler: DefaultHandler{}})

	chans := CreateChannels(g)

	if _, ok := chans[ChannelKey{0, 0}]; !ok {
		t.Error("expected channel for source output (node 0, port 0)")
	}
	// Sink has no output channel.
	if _, ok := chans[ChannelKey{1, 0}]; ok {
		t.Error("sink should not have an output channel")
	}
}

func TestCreateChannels_Partition(t *testing.T) {
	g := New()
	g.AddNode(&Node{Kind: Source, Buffer: 8, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: Partition, Inputs: []InputRef{{0, 0}}, Buffer: 8, ErrorHandler: DefaultHandler{}})

	chans := CreateChannels(g)

	if _, ok := chans[ChannelKey{1, 0}]; !ok {
		t.Error("expected match channel (node 1, port 0)")
	}
	if _, ok := chans[ChannelKey{1, 1}]; !ok {
		t.Error("expected rest channel (node 1, port 1)")
	}
}

func TestCreateChannels_Broadcast(t *testing.T) {
	const n = 3
	g := New()
	g.AddNode(&Node{Kind: Source, Buffer: 8, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: BroadcastNode, BroadcastN: n, Inputs: []InputRef{{0, 0}}, Buffer: 8, ErrorHandler: DefaultHandler{}})

	chans := CreateChannels(g)

	for i := range n {
		if _, ok := chans[ChannelKey{1, i}]; !ok {
			t.Errorf("expected broadcast channel port %d", i)
		}
	}
}

func TestCreateChannels_BufferSize(t *testing.T) {
	g := New()
	g.AddNode(&Node{Kind: Source, Buffer: 64, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{0, 0}}, ErrorHandler: DefaultHandler{}})

	chans := CreateChannels(g)

	ch := chans[ChannelKey{0, 0}]
	if cap(ch) != 64 {
		t.Errorf("expected buffer capacity 64, got %d", cap(ch))
	}
}

func TestCreateChannels_DefaultBuffer(t *testing.T) {
	g := New()
	g.AddNode(&Node{Kind: Source, Buffer: 0, ErrorHandler: DefaultHandler{}}) // 0 → use default
	g.AddNode(&Node{Kind: Sink, Inputs: []InputRef{{0, 0}}, ErrorHandler: DefaultHandler{}})

	chans := CreateChannels(g)

	ch := chans[ChannelKey{0, 0}]
	if cap(ch) != DefaultBuffer {
		t.Errorf("expected default buffer capacity %d, got %d", DefaultBuffer, cap(ch))
	}
}

// ---------------------------------------------------------------------------
// Run — basic execution
// ---------------------------------------------------------------------------

func TestRun_SourceToSink(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	var out []any
	collectSink(g, src, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d: %v", len(out), out)
	}
}

func TestRun_MapTransform(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	mapID := g.AddNode(&Node{
		Kind:         Map,
		Fn:           func(_ context.Context, item any) (any, error) { return item.(int) * 10, nil },
		Inputs:       []InputRef{{src, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, mapID, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []any{10, 20, 30}
	for i, v := range out {
		if v != want[i] {
			t.Errorf("item %d: want %v, got %v", i, want[i], v)
		}
	}
}

func TestRun_FilterGate(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3, 4, 5), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	filterID := g.AddNode(&Node{
		Kind:         Filter,
		Fn:           func(item any) bool { return item.(int)%2 == 0 },
		Inputs:       []InputRef{{src, 0}},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, filterID, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0] != 2 || out[1] != 4 {
		t.Errorf("unexpected filter output: %v", out)
	}
}

func TestRun_FlatMap(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	fmID := g.AddNode(&Node{
		Kind: FlatMap,
		Fn: func(_ context.Context, item any, yield func(any) error) error {
			n := item.(int)
			for i := range n {
				if err := yield(i + 1); err != nil {
					return err
				}
			}
			return nil
		},
		Inputs:       []InputRef{{src, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, fmID, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1→[1], 2→[1,2], 3→[1,2,3] = 6 items total
	if len(out) != 6 {
		t.Errorf("expected 6 items, got %d: %v", len(out), out)
	}
}

func TestRun_TakeLimit(t *testing.T) {
	// Source produces many items; Take(2) should stop early.
	g := New()
	src := g.AddNode(&Node{
		Kind: Source,
		Fn: func(_ context.Context, yield func(any) bool) error {
			for i := range 1000 {
				if !yield(i) {
					return nil
				}
			}
			return nil
		},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	takeID := g.AddNode(&Node{
		Kind:         Take,
		TakeN:        2,
		Inputs:       []InputRef{{src, 0}},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, takeID, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 items, got %d", len(out))
	}
}

// ---------------------------------------------------------------------------
// Run — error handling
// ---------------------------------------------------------------------------

func TestRun_ErrorHalt(t *testing.T) {
	boom := errors.New("boom")
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{
		Kind:         Sink,
		Fn:           func(_ context.Context, _ any) error { return boom },
		Inputs:       []InputRef{{src, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})

	err := run(g)
	if err == nil {
		t.Fatal("expected error")
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StageError, got %T: %v", err, err)
	}
	if !errors.Is(se.Cause, boom) {
		t.Errorf("expected cause to be boom, got %v", se.Cause)
	}
}

func TestRun_ErrorSkip(t *testing.T) {
	boom := errors.New("boom")
	var processed atomic.Int32
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{
		Kind: Map,
		Fn: func(_ context.Context, item any) (any, error) {
			if item.(int) == 2 {
				return nil, boom
			}
			return item, nil
		},
		Inputs:       []InputRef{{src, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: &skipHandler{},
	})
	var out []any
	mapID := len(g.Nodes) - 1
	g.AddNode(&Node{
		Kind:         Sink,
		Fn:           func(_ context.Context, item any) error { processed.Add(1); out = append(out, item); return nil },
		Inputs:       []InputRef{{mapID, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed.Load() != 2 {
		t.Errorf("expected 2 items to pass through, got %d", processed.Load())
	}
}

// skipHandler is a local test ErrorHandler that skips all errors.
type skipHandler struct{}

func (*skipHandler) Handle(error, int) ErrorAction    { return ActionSkip }
func (*skipHandler) Backoff() func(int) time.Duration { return nil }

// ---------------------------------------------------------------------------
// Run — concurrency
// ---------------------------------------------------------------------------

func TestRun_ConcurrentMap_AllItemsProcessed(t *testing.T) {
	const n = 100
	items := make([]any, n)
	for i := range n {
		items[i] = i
	}

	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(items...), Buffer: 32, ErrorHandler: DefaultHandler{}})
	mapID := g.AddNode(&Node{
		Kind:         Map,
		Fn:           func(_ context.Context, item any) (any, error) { return item.(int) * 2, nil },
		Inputs:       []InputRef{{src, 0}},
		Concurrency:  8,
		Buffer:       32,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	var mu sync.Mutex
	g.AddNode(&Node{
		Kind:         Sink,
		Fn:           func(_ context.Context, item any) error { mu.Lock(); out = append(out, item); mu.Unlock(); return nil },
		Inputs:       []InputRef{{mapID, 0}},
		Concurrency:  1,
		Buffer:       32,
		ErrorHandler: DefaultHandler{},
	})

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != n {
		t.Fatalf("expected %d items, got %d", n, len(out))
	}
	// All values must be even doubles.
	sort.Slice(out, func(i, j int) bool { return out[i].(int) < out[j].(int) })
	for i, v := range out {
		if v.(int) != i*2 {
			t.Errorf("item %d: expected %d, got %d", i, i*2, v.(int))
		}
	}
}

// ---------------------------------------------------------------------------
// Run — fan-out and fan-in
// ---------------------------------------------------------------------------

func TestRun_Partition(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3, 4, 5), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	partID := g.AddNode(&Node{
		Kind:         Partition,
		Fn:           func(item any) bool { return item.(int)%2 == 0 }, // evens → port 0
		Inputs:       []InputRef{{src, 0}},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})

	var evens, odds []any
	var mu sync.Mutex
	g.AddNode(&Node{Kind: Sink, Fn: func(_ context.Context, item any) error {
		mu.Lock()
		evens = append(evens, item)
		mu.Unlock()
		return nil
	}, Inputs: []InputRef{{partID, 0}}, Concurrency: 1, Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	g.AddNode(&Node{Kind: Sink, Fn: func(_ context.Context, item any) error { mu.Lock(); odds = append(odds, item); mu.Unlock(); return nil }, Inputs: []InputRef{{partID, 1}}, Concurrency: 1, Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evens) != 2 {
		t.Errorf("expected 2 evens, got %d: %v", len(evens), evens)
	}
	if len(odds) != 3 {
		t.Errorf("expected 3 odds, got %d: %v", len(odds), odds)
	}
}

func TestRun_Merge(t *testing.T) {
	g := New()
	src1 := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	src2 := g.AddNode(&Node{Kind: Source, Fn: sourceFn(3, 4), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	mergeID := g.AddNode(&Node{
		Kind:         Merge,
		Inputs:       []InputRef{{src1, 0}, {src2, 0}},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, mergeID, &out)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 4 {
		t.Errorf("expected 4 merged items, got %d: %v", len(out), out)
	}
}

// ---------------------------------------------------------------------------
// Run — batching
// ---------------------------------------------------------------------------

func TestRun_Batch(t *testing.T) {
	g := New()
	src := g.AddNode(&Node{Kind: Source, Fn: sourceFn(1, 2, 3, 4, 5), Buffer: DefaultBuffer, ErrorHandler: DefaultHandler{}})
	batchID := g.AddNode(&Node{
		Kind:      Batch,
		BatchSize: 2,
		BatchConvert: func(items []any) any {
			out := make([]int, len(items))
			for i, v := range items {
				out[i] = v.(int)
			}
			return out
		},
		Inputs:       []InputRef{{src, 0}},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var batches []any
	collectSink(g, batchID, &batches)

	if err := run(g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 5 items with batch size 2 → [1,2], [3,4], [5]
	if len(batches) != 3 {
		t.Errorf("expected 3 batches, got %d: %v", len(batches), batches)
	}
}

// ---------------------------------------------------------------------------
// Run — context cancellation
// ---------------------------------------------------------------------------

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	g := New()
	var started atomic.Bool
	g.AddNode(&Node{
		Kind: Source,
		Fn: func(ctx context.Context, yield func(any) bool) error {
			started.Store(true)
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				if !yield(i) {
					return nil
				}
			}
		},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	var out []any
	collectSink(g, 0, &out)

	// Cancel after a short delay.
	go func() {
		for !started.Load() {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	err := Run(ctx, g, RunConfig{})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Run — graceful drain
// ---------------------------------------------------------------------------

func TestRun_GracefulDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const total = 20
	var received atomic.Int32

	g := New()
	g.AddNode(&Node{
		Kind: Source,
		Fn: func(ctx context.Context, yield func(any) bool) error {
			for i := range total {
				if !yield(i) {
					return nil
				}
			}
			return nil
		},
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})
	g.AddNode(&Node{
		Kind: Sink,
		Fn: func(_ context.Context, _ any) error {
			received.Add(1)
			return nil
		},
		Inputs:       []InputRef{{0, 0}},
		Concurrency:  1,
		Buffer:       DefaultBuffer,
		ErrorHandler: DefaultHandler{},
	})

	cancel() // cancel immediately — drain should still let in-flight items through

	err := Run(ctx, g, RunConfig{DrainTimeout: 500 * time.Millisecond})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Graph — state management
// ---------------------------------------------------------------------------

func TestGraph_InitRefs_MemoryOnly(t *testing.T) {
	g := New()
	g.RegisterKey("counter", func(store Store, codec Codec) any {
		return &struct{ n int }{}
	})

	g.InitRefs(nil, JSONCodec{})

	ref := g.GetRef("counter")
	if ref == nil {
		t.Fatal("expected ref to be initialized")
	}
}

func TestGraph_InitRefs_WithStore(t *testing.T) {
	store := &stubStore{}
	var gotStore Store
	g := New()
	g.RegisterKey("k", func(s Store, codec Codec) any {
		gotStore = s
		return struct{}{}
	})

	g.InitRefs(store, JSONCodec{})

	if gotStore != store {
		t.Error("factory did not receive the configured store")
	}
}

func TestGraph_RegisterKey_FirstWins(t *testing.T) {
	var calls int
	g := New()
	g.RegisterKey("k", func(Store, Codec) any { calls++; return "first" })
	g.RegisterKey("k", func(Store, Codec) any { calls++; return "second" })

	g.InitRefs(nil, JSONCodec{})

	if v := g.GetRef("k"); v != "first" {
		t.Errorf("expected first factory to win, got %v", v)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 factory call, got %d", calls)
	}
}

// stubStore is a minimal Store for testing; all ops succeed but do nothing.
type stubStore struct{}

func (*stubStore) Get(_ context.Context, _ string) ([]byte, bool, error) { return nil, false, nil }
func (*stubStore) Set(_ context.Context, _ string, _ []byte) error       { return nil }
func (*stubStore) Delete(_ context.Context, _ string) error              { return nil }

// ---------------------------------------------------------------------------
// ProcessItem — retry loop
// ---------------------------------------------------------------------------

func TestProcessItem_Halt(t *testing.T) {
	boom := errors.New("boom")
	fn := func(_ context.Context, _ any) (any, error) { return nil, boom }
	_, err, attempt := ProcessItem(context.Background(), fn, nil, DefaultHandler{})
	if !errors.Is(err, boom) {
		t.Errorf("expected boom, got %v", err)
	}
	if attempt != 0 {
		t.Errorf("expected attempt 0, got %d", attempt)
	}
}

func TestProcessItem_Skip(t *testing.T) {
	boom := errors.New("boom")
	fn := func(_ context.Context, _ any) (any, error) { return nil, boom }
	_, err, _ := ProcessItem(context.Background(), fn, nil, &skipHandler{})
	if !errors.Is(err, ErrSkipped) {
		t.Errorf("expected ErrSkipped, got %v", err)
	}
}

func TestProcessItem_Retry(t *testing.T) {
	var calls atomic.Int32
	fn := func(_ context.Context, _ any) (any, error) {
		n := calls.Add(1)
		if n < 3 {
			return nil, fmt.Errorf("transient")
		}
		return "ok", nil
	}
	result, err, attempt := ProcessItem(context.Background(), fn, nil, &retryNHandler{max: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected result 'ok', got %v", result)
	}
	if attempt != 2 {
		t.Errorf("expected attempt 2, got %d", attempt)
	}
}

// retryNHandler retries up to max times with zero backoff delay.
type retryNHandler struct{ max int }

func (h *retryNHandler) Handle(_ error, attempt int) ErrorAction {
	if attempt < h.max {
		return ActionRetry
	}
	return ActionHalt
}
func (h *retryNHandler) Backoff() func(int) time.Duration {
	return func(int) time.Duration { return 0 }
}

// ---------------------------------------------------------------------------
// Outbox — overflow strategies
// ---------------------------------------------------------------------------

func TestOutbox_Block(t *testing.T) {
	ch := make(chan any, 2)
	ob := NewOutbox(ch, OverflowBlock, NoopHook{}, "test")

	if err := ob.Send(context.Background(), 1); err != nil {
		t.Fatalf("unexpected send error: %v", err)
	}
	if ob.Dropped() != 0 {
		t.Errorf("expected 0 drops, got %d", ob.Dropped())
	}
}

func TestOutbox_DropNewest(t *testing.T) {
	ch := make(chan any, 1)
	ch <- "existing" // fill the buffer
	ob := NewOutbox(ch, OverflowDropNewest, NoopHook{}, "test")

	ob.Send(context.Background(), "new") //nolint:errcheck

	if ob.Dropped() != 1 {
		t.Errorf("expected 1 drop, got %d", ob.Dropped())
	}
	// Channel should still hold the original item.
	if got := <-ch; got != "existing" {
		t.Errorf("expected 'existing', got %v", got)
	}
}

func TestOutbox_DropOldest(t *testing.T) {
	ch := make(chan any, 1)
	ch <- "existing" // fill the buffer
	ob := NewOutbox(ch, OverflowDropOldest, NoopHook{}, "test")

	ob.Send(context.Background(), "new") //nolint:errcheck

	if ob.Dropped() != 1 {
		t.Errorf("expected 1 drop, got %d", ob.Dropped())
	}
	// Channel should now hold the new item.
	if got := <-ch; got != "new" {
		t.Errorf("expected 'new', got %v", got)
	}
}
