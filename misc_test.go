package kitsune_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/testkit"
)

// ---------------------------------------------------------------------------
// 3a. Contains
// ---------------------------------------------------------------------------

func TestContains_Present(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	got, err := kitsune.Contains(context.Background(), p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Contains returned false, want true")
	}
}

func TestContains_Absent(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Contains(context.Background(), p, 99)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("Contains returned true, want false")
	}
}

func TestContains_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got, err := kitsune.Contains(context.Background(), p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("Contains on empty stream returned true, want false")
	}
}

// ---------------------------------------------------------------------------
// 3b. ElementAt
// ---------------------------------------------------------------------------

func TestElementAt_Zero(t *testing.T) {
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	got, ok, err := kitsune.ElementAt(context.Background(), p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "a" {
		t.Errorf("got (%q, %v), want (\"a\", true)", got, ok)
	}
}

func TestElementAt_Valid(t *testing.T) {
	p := kitsune.FromSlice([]string{"a", "b", "c"})
	got, ok, err := kitsune.ElementAt(context.Background(), p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "c" {
		t.Errorf("got (%q, %v), want (\"c\", true)", got, ok)
	}
}

func TestElementAt_OutOfBounds(t *testing.T) {
	p := kitsune.FromSlice([]int{10, 20})
	got, ok, err := kitsune.ElementAt(context.Background(), p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != 0 {
		t.Errorf("got (%d, %v), want (0, false)", got, ok)
	}
}

func TestElementAt_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got, ok, err := kitsune.ElementAt(context.Background(), p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != 0 {
		t.Errorf("got (%d, %v), want (0, false)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// 3c. DefaultIfEmpty
// ---------------------------------------------------------------------------

func TestDefaultIfEmpty_NonEmpty(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := testkit.MustCollect(t, kitsune.DefaultIfEmpty(p, 99))
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("got %v, want [1 2 3]", got)
	}
}

func TestDefaultIfEmpty_Empty(t *testing.T) {
	p := kitsune.FromSlice([]int{})
	got := testkit.MustCollect(t, kitsune.DefaultIfEmpty(p, 42))
	if len(got) != 1 || got[0] != 42 {
		t.Errorf("got %v, want [42]", got)
	}
}

func TestDefaultIfEmpty_SingleItem(t *testing.T) {
	p := kitsune.FromSlice([]int{7})
	got := testkit.MustCollect(t, kitsune.DefaultIfEmpty(p, 99))
	if len(got) != 1 || got[0] != 7 {
		t.Errorf("got %v, want [7]", got)
	}
}

// ---------------------------------------------------------------------------
// 3d. Timestamp / Timestamped[T]
// ---------------------------------------------------------------------------

func TestTimestamp_PopulatesTime(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	before := time.Now()
	items := testkit.MustCollect(t, kitsune.Timestamp(p))
	after := time.Now()

	for i, ts := range items {
		if ts.Time.IsZero() {
			t.Errorf("item %d: Time is zero", i)
		}
		if ts.Time.Before(before) || ts.Time.After(after) {
			t.Errorf("item %d: Time %v not in [%v, %v]", i, ts.Time, before, after)
		}
	}
}

func TestTimestamp_OrderPreserved(t *testing.T) {
	p := kitsune.FromSlice([]int{10, 20, 30})
	items := testkit.MustCollect(t, kitsune.Timestamp(p))
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	for i, ts := range items {
		if ts.Value != (i+1)*10 {
			t.Errorf("item %d: Value=%d, want %d", i, ts.Value, (i+1)*10)
		}
	}
}

// ---------------------------------------------------------------------------
// 3e. TimeInterval / TimedInterval[T]
// ---------------------------------------------------------------------------

func TestTimeInterval_Basic(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	items := testkit.MustCollect(t, kitsune.TimeInterval(p))
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0].Elapsed != 0 {
		t.Errorf("first item Elapsed=%v, want 0", items[0].Elapsed)
	}
	for i, it := range items {
		if it.Value != i+1 {
			t.Errorf("item %d: Value=%d, want %d", i, it.Value, i+1)
		}
	}
}

func TestTimeInterval_Sequential(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	items := testkit.MustCollect(t, kitsune.TimeInterval(p))
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	// First item always has Elapsed==0; subsequent items have Elapsed >= 0.
	if items[0].Elapsed != 0 {
		t.Errorf("first item Elapsed=%v, want 0", items[0].Elapsed)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Elapsed < 0 {
			t.Errorf("item %d: Elapsed=%v, want >= 0", i, items[i].Elapsed)
		}
	}
}

// ---------------------------------------------------------------------------
// 3f. ZipWith
// ---------------------------------------------------------------------------

func TestZipWith_Basic(t *testing.T) {
	// Two independent sources zipped together within a single Run.
	as := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
		kitsune.WithName("as"))
	bs := kitsune.Map(kitsune.FromSlice([]int{10, 20, 30}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("bs"))
	combined := kitsune.ZipWith(as, bs, func(_ context.Context, a, b int) (int, error) {
		return a + b, nil
	})
	got := testkit.MustCollect(t, combined)
	if len(got) != 3 {
		t.Errorf("got %d items, want 3", len(got))
	}
}

func TestZipWith_IndependentGraphs(t *testing.T) {
	// Two entirely separate sources, combined by ZipWith.
	as := kitsune.FromSlice([]int{1, 2, 3})
	bs := kitsune.FromSlice([]string{"x", "y", "z"})
	combined := kitsune.ZipWith(as, bs, func(_ context.Context, n int, s string) (string, error) {
		return s, nil
	})
	got := testkit.MustCollect(t, combined)
	if len(got) != 3 {
		t.Errorf("got %d items, want 3: %v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// 3g. WithSampleRate
// ---------------------------------------------------------------------------

func TestWithSampleRate_Fires(t *testing.T) {
	// WithSampleRate is accepted as a RunOption and must not cause an error.
	// Sampling fires OnItemSample on the hook when implemented; verify no panic.
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("sampled"),
	)
	items, _ := testkit.MustCollectWithHook(t, p, kitsune.WithSampleRate(1))
	if len(items) != 4 {
		t.Errorf("expected 4 items, got %d", len(items))
	}
}

func TestWithSampleRate_Disabled(t *testing.T) {
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3, 4}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("nosamples"),
	)
	// Negative rate disables sampling.
	items, hook := testkit.MustCollectWithHook(t, p, kitsune.WithSampleRate(-1))
	_ = items
	for _, ev := range hook.Items() {
		if ev.IsSample {
			t.Error("WithSampleRate(-1): got sample event, want none")
		}
	}
}

// ---------------------------------------------------------------------------
// 3h. GraphNode metadata
// ---------------------------------------------------------------------------

func TestGraphNodeMetadata(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("meta-stage"),
		kitsune.Concurrency(3),
		kitsune.Buffer(8),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	graph := hook.Graph()
	if len(graph) == 0 {
		t.Fatal("expected graph nodes")
	}
	var found bool
	for _, n := range graph {
		if n.Name == "meta-stage" {
			found = true
			if n.Kind == "" {
				t.Error("Kind is empty")
			}
			if n.Concurrency != 3 {
				t.Errorf("Concurrency=%d, want 3", n.Concurrency)
			}
			if n.Buffer != 8 {
				t.Errorf("Buffer=%d, want 8", n.Buffer)
			}
		}
	}
	if !found {
		t.Error("node 'meta-stage' not found in graph")
	}
}

func TestGraphNodeBatchSize(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Batch(
		kitsune.FromSlice([]int{1, 2, 3}),
		4,
		kitsune.WithName("batcher"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	graph := hook.Graph()
	var found bool
	for _, n := range graph {
		if n.Name == "batcher" {
			found = true
			if n.BatchSize != 4 {
				t.Errorf("BatchSize=%d, want 4", n.BatchSize)
			}
		}
	}
	if !found {
		t.Error("node 'batcher' not found in graph")
	}
}

func TestGraphNodeHasSupervision(t *testing.T) {
	hook := &testkit.RecordingHook{}
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.WithName("supervised"),
		kitsune.Supervise(kitsune.RestartOnError(2, nil)),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(hook)); err != nil {
		t.Fatal(err)
	}
	graph := hook.Graph()
	var found bool
	for _, n := range graph {
		if n.Name == "supervised" {
			found = true
			if !n.HasSupervision {
				t.Error("HasSupervision=false, want true")
			}
		}
	}
	if !found {
		t.Error("node 'supervised' not found in graph")
	}
}

// ---------------------------------------------------------------------------
// 3i. StageError fields
// ---------------------------------------------------------------------------

func TestStageErrorWrapping(t *testing.T) {
	boom := errors.New("underlying error")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) { return 0, boom },
		kitsune.WithName("err-stage"),
	)
	_, err := p.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *kitsune.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StageError, got %T: %v", err, err)
	}
	if se.Stage != "err-stage" {
		t.Errorf("Stage=%q, want %q", se.Stage, "err-stage")
	}
	if !errors.Is(se, boom) {
		t.Errorf("Unwrap does not chain to original error")
	}
}

func TestStageErrorWithRetry(t *testing.T) {
	// Use per-item retry via OnError(Retry(...)) — Attempt increments on each retry.
	var calls atomic.Int32
	boom := errors.New("retryable")
	p := kitsune.Map(
		kitsune.FromSlice([]int{1}),
		func(_ context.Context, n int) (int, error) {
			calls.Add(1)
			return 0, boom
		},
		kitsune.WithName("retry-stage"),
		kitsune.OnError(kitsune.RetryMax(2, kitsune.FixedBackoff(0))),
	)
	_, err := p.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var se *kitsune.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StageError, got %T", err)
	}
	// After 2 retries the fn was called 3 times total; Attempt should be >= 2.
	if se.Attempt < 2 {
		t.Errorf("Attempt=%d, expected >= 2 after 2 retries", se.Attempt)
	}
}

func TestContextErrNotWrapped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) {
			cancel()
			return n, nil
		},
	)
	_, err := p.Collect(ctx)
	if err == nil {
		return // may complete before cancel; acceptable
	}
	var se *kitsune.StageError
	if errors.As(err, &se) {
		t.Errorf("context.Canceled wrapped in StageError: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3j. Codec / WithCodec
// ---------------------------------------------------------------------------

// countingCodec wraps JSONCodec and counts Marshal/Unmarshal calls.
type countingCodec struct {
	marshals   atomic.Int32
	unmarshals atomic.Int32
}

func (c *countingCodec) Marshal(v any) ([]byte, error) {
	c.marshals.Add(1)
	return kitsune.JSONCodec{}.Marshal(v)
}

func (c *countingCodec) Unmarshal(data []byte, v any) error {
	c.unmarshals.Add(1)
	return kitsune.JSONCodec{}.Unmarshal(data, v)
}

func TestCodecDefaultJSON(t *testing.T) {
	// Store-backed run with the default JSON codec should work transparently.
	key := kitsune.NewKey("codec-default", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{1, 2, 3}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	got, err := p.Collect(context.Background(), kitsune.WithStore(kitsune.MemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	// Running totals: 1, 3, 6.
	if len(got) != 3 || got[2] != 6 {
		t.Errorf("got %v, want [..., 6]", got)
	}
}

func TestCodecCustom(t *testing.T) {
	// Custom codec must be called when the store does NOT implement InProcessStore.
	// MemoryStore bypasses codec via InProcessStore; use plainStore to test the codec path.
	codec := &countingCodec{}
	key := kitsune.NewKey("codec-custom", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{1, 2, 3}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	_, err := p.Collect(context.Background(),
		kitsune.WithStore(newPlainStore()),
		kitsune.WithCodec(codec),
	)
	if err != nil {
		t.Fatal(err)
	}
	if codec.marshals.Load() == 0 {
		t.Error("custom codec Marshal was never called")
	}
}

func TestCodecStore(t *testing.T) {
	// Explicit JSONCodec + MemoryStore should produce correct results.
	key := kitsune.NewKey("codec-store", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{10, 20, 30}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	got, err := p.Collect(context.Background(),
		kitsune.WithStore(kitsune.MemoryStore()),
		kitsune.WithCodec(kitsune.JSONCodec{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != 60 {
		t.Errorf("got %v, want [..., 60]", got)
	}
}

// ---------------------------------------------------------------------------
// 3k. MultiHook
// ---------------------------------------------------------------------------

func TestMultiHook_EventCounts(t *testing.T) {
	h1 := &testkit.RecordingHook{}
	h2 := &testkit.RecordingHook{}
	multi := kitsune.MultiHook(h1, h2)

	p := kitsune.Map(
		kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, n int) (int, error) { return n * 2, nil },
		kitsune.WithName("mstage"),
	)
	if err := p.Drain().Run(context.Background(), kitsune.WithHook(multi)); err != nil {
		t.Fatal(err)
	}

	if len(h1.Items()) == 0 {
		t.Error("h1: no item events")
	}
	if len(h2.Items()) == 0 {
		t.Error("h2: no item events")
	}
	if len(h1.Items()) != len(h2.Items()) {
		t.Errorf("h1 items=%d, h2 items=%d — both should match", len(h1.Items()), len(h2.Items()))
	}
	if len(h1.Dones()) == 0 || len(h2.Dones()) == 0 {
		t.Error("both hooks should receive StageDone events")
	}
}

func TestMultiHook_DropEvents(t *testing.T) {
	h1 := &testkit.RecordingHook{} // implements OverflowHook
	h2 := &testkit.RecordingHook{} // implements OverflowHook
	multi := kitsune.MultiHook(h1, h2)

	// Fast producer, small buffer with DropNewest, slow consumer.
	items := make([]int, 50)
	p := kitsune.Map(kitsune.FromSlice(items),
		func(_ context.Context, n int) (int, error) { return n, nil },
		kitsune.Buffer(1),
		kitsune.Overflow(kitsune.DropNewest),
	)
	if err := p.ForEach(func(_ context.Context, _ int) error {
		time.Sleep(time.Millisecond)
		return nil
	}).Run(context.Background(), kitsune.WithHook(multi)); err != nil {
		t.Fatal(err)
	}

	// Both hooks must see the same drop count (may be 0 on fast machines).
	if len(h1.Drops()) != len(h2.Drops()) {
		t.Errorf("drop count mismatch: h1=%d h2=%d", len(h1.Drops()), len(h2.Drops()))
	}
}

// ---------------------------------------------------------------------------
// 3m. LiftPure
// ---------------------------------------------------------------------------

func TestLiftPure_Basic(t *testing.T) {
	double := kitsune.LiftPure(func(n int) int { return n * 2 })
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}), double)
	testkit.CollectAndExpect(t, p, []int{2, 4, 6})
}

func TestLiftPure_TypeChange(t *testing.T) {
	toString := kitsune.LiftPure(func(n int) string {
		return string(rune('a' + n - 1))
	})
	p := kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}), toString)
	testkit.CollectAndExpect(t, p, []string{"a", "b", "c"})
}

// ---------------------------------------------------------------------------
// 3n. InProcessStore bypass
// ---------------------------------------------------------------------------

func TestMemoryStore_InProcessBypass(t *testing.T) {
	// MemoryStore should store and retrieve typed values without codec
	// when accessed via kitsune.MapWith — codec must NOT be called.
	panicCodec := &panicOnCallCodec{}
	key := kitsune.NewKey[int]("ips-bypass", 0)
	p := kitsune.MapWith(
		kitsune.FromSlice([]int{1, 2, 3}),
		key,
		func(ctx context.Context, ref *kitsune.Ref[int], n int) (int, error) {
			return ref.UpdateAndGet(ctx, func(acc int) (int, error) { return acc + n, nil })
		},
	)
	got, err := p.Collect(context.Background(),
		kitsune.WithStore(kitsune.MemoryStore()),
		kitsune.WithCodec(panicCodec),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 3, 6}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d: got %d, want %d", i, got[i], w)
		}
	}
}

// panicOnCallCodec panics immediately if Marshal or Unmarshal is called.
// Used to verify InProcessStore paths bypass codec entirely.
type panicOnCallCodec struct{}

func (panicOnCallCodec) Marshal(v any) ([]byte, error) {
	panic("codec.Marshal called on InProcessStore path; bypass is broken")
}

func (panicOnCallCodec) Unmarshal(data []byte, v any) error {
	panic("codec.Unmarshal called on InProcessStore path; bypass is broken")
}

// plainStore implements Store but not InProcessStore.
// Used to test that the codec path is exercised for non-in-process stores.
type plainStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func newPlainStore() *plainStore {
	return &plainStore{values: make(map[string][]byte)}
}

func (s *plainStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *plainStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *plainStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}
