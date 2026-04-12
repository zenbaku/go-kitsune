package kitsune_test

import (
	"context"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ctxKey is a private key type for test context values.
type ctxKey string

// contextCarrierMsg implements kitsune.ContextCarrier for testing precedence.
type contextCarrierMsg struct {
	traceID    string
	carrierCtx context.Context
}

func (m *contextCarrierMsg) Context() context.Context { return m.carrierCtx }

// ---------------------------------------------------------------------------
// WithContextMapper — Map
// ---------------------------------------------------------------------------

// TestWithContextMapper_Map verifies that WithContextMapper propagates
// per-item context values into the stage function on a serial Map stage.
func TestWithContextMapper_Map(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	items := []msg{{1, "span-a"}, {2, "span-b"}, {3, "span-c"}}
	p := kitsune.FromSlice(items)

	var got []string
	out := kitsune.Map(p, func(ctx context.Context, m msg) (string, error) {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		got = append(got, v)
		return v, nil
	}, kitsune.WithContextMapper(mapper))

	collectAll(t, out)

	want := []string{"span-a", "span-b", "span-c"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestWithContextMapper_Map_Concurrent verifies mapper works with Concurrency > 1.
func TestWithContextMapper_Map_Concurrent(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	items := make([]msg, 8)
	for i := range items {
		items[i] = msg{i, "span-" + string(rune('a'+i))}
	}
	p := kitsune.FromSlice(items)

	var (
		mismatch atomic.Int32
	)
	out := kitsune.Map(p, func(ctx context.Context, m msg) (int, error) {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		expected := "span-" + string(rune('a'+m.id))
		if v != expected {
			mismatch.Add(1)
		}
		return m.id, nil
	}, kitsune.WithContextMapper(mapper), kitsune.Concurrency(4))

	collectAll(t, out)

	if mismatch.Load() > 0 {
		t.Fatalf("context values did not match items in %d cases", mismatch.Load())
	}
}

// TestWithContextMapper_Map_PrecedenceOverCarrier verifies that WithContextMapper
// takes precedence over the ContextCarrier interface when both are present.
func TestWithContextMapper_Map_PrecedenceOverCarrier(t *testing.T) {
	// contextCarrierMsg.Context() returns a ctx with source="carrier".
	// The mapper returns a ctx with source="mapper".
	// The stage function should see "mapper" (mapper wins).
	carrierCtx := context.WithValue(context.Background(), ctxKey("source"), "carrier")
	mapperCtx := context.WithValue(context.Background(), ctxKey("source"), "mapper")

	items := []*contextCarrierMsg{
		{traceID: "span-1", carrierCtx: carrierCtx},
	}

	p := kitsune.FromSlice(items)
	mapper := func(m *contextCarrierMsg) context.Context {
		return mapperCtx
	}

	var got string
	out := kitsune.Map(p, func(ctx context.Context, m *contextCarrierMsg) (string, error) {
		got, _ = ctx.Value(ctxKey("source")).(string)
		return got, nil
	}, kitsune.WithContextMapper(mapper))

	collectAll(t, out)

	if got != "mapper" {
		t.Fatalf("expected mapper context value, got %q", got)
	}
}

// TestWithContextMapper_Map_NilReturn verifies that when the mapper returns nil,
// the stage context is used unchanged (no panic, no value injection).
func TestWithContextMapper_Map_NilReturn(t *testing.T) {
	mapper := func(v int) context.Context { return nil }

	p := kitsune.FromSlice([]int{1, 2, 3})
	var keys []string
	out := kitsune.Map(p, func(ctx context.Context, v int) (int, error) {
		k, _ := ctx.Value(ctxKey("trace")).(string)
		keys = append(keys, k)
		return v, nil
	}, kitsune.WithContextMapper(mapper))

	collectAll(t, out)

	for _, k := range keys {
		if k != "" {
			t.Fatalf("expected empty context value, got %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// WithContextMapper — FlatMap
// ---------------------------------------------------------------------------

// TestWithContextMapper_FlatMap verifies mapper propagation on a FlatMap stage.
func TestWithContextMapper_FlatMap(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	items := []msg{{1, "span-a"}, {2, "span-b"}}
	p := kitsune.FromSlice(items)

	var got []string
	out := kitsune.FlatMap(p, func(ctx context.Context, m msg, yield func(string) error) error {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		return yield(v)
	}, kitsune.WithContextMapper(mapper))

	got = collectAll(t, out)
	want := []string{"span-a", "span-b"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestWithContextMapper_FlatMap_Concurrent verifies mapper works with concurrent FlatMap.
func TestWithContextMapper_FlatMap_Concurrent(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	const n = 6
	items := make([]msg, n)
	for i := range items {
		items[i] = msg{i, "span-" + string(rune('a'+i))}
	}

	p := kitsune.FromSlice(items)
	var mismatches atomic.Int32
	out := kitsune.FlatMap(p, func(ctx context.Context, m msg, yield func(int) error) error {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		expected := "span-" + string(rune('a'+m.id))
		if v != expected {
			mismatches.Add(1)
		}
		return yield(m.id)
	}, kitsune.WithContextMapper(mapper), kitsune.Concurrency(3))

	collectAll(t, out)
	if mismatches.Load() > 0 {
		t.Fatalf("context mismatch in %d FlatMap items", mismatches.Load())
	}
}

// ---------------------------------------------------------------------------
// WithContextMapper — ForEach
// ---------------------------------------------------------------------------

// TestWithContextMapper_ForEach verifies mapper propagation in a ForEach terminal.
func TestWithContextMapper_ForEach(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	items := []msg{{1, "span-a"}, {2, "span-b"}, {3, "span-c"}}
	p := kitsune.FromSlice(items)

	var got []string
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := p.ForEach(func(ctx context.Context, m msg) error {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		got = append(got, v)
		return nil
	}, kitsune.WithContextMapper(mapper)).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"span-a", "span-b", "span-c"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestWithContextMapper_ForEach_Concurrent verifies mapper with concurrent ForEach.
func TestWithContextMapper_ForEach_Concurrent(t *testing.T) {
	type msg struct {
		id      int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	const n = 8
	items := make([]msg, n)
	for i := range items {
		items[i] = msg{i, "span-" + string(rune('a'+i))}
	}

	p := kitsune.FromSlice(items)
	var mismatches atomic.Int32
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := p.ForEach(func(ctx context.Context, m msg) error {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		expected := "span-" + string(rune('a'+m.id))
		if v != expected {
			mismatches.Add(1)
		}
		return nil
	}, kitsune.WithContextMapper(mapper), kitsune.Concurrency(4)).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if mismatches.Load() > 0 {
		t.Fatalf("context mismatch in %d ForEach items", mismatches.Load())
	}
}

// ---------------------------------------------------------------------------
// WithContextMapper — fast path disqualification
// ---------------------------------------------------------------------------

// TestWithContextMapper_DisablesFastPath verifies that a stage with
// WithContextMapper still produces correct output (fast path is disqualified,
// slow path is used instead) and does not regress other behaviour.
func TestWithContextMapper_DisablesFastPath(t *testing.T) {
	type msg struct {
		v       int
		traceID string
	}

	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}

	items := []msg{{1, "span-1"}, {2, "span-2"}, {3, "span-3"}}
	p := kitsune.FromSlice(items)
	out := kitsune.Map(p, func(ctx context.Context, m msg) (int, error) {
		// Stage would normally qualify for fast path (serial, default handler,
		// no overflow, no timeout, no hook), but WithContextMapper disqualifies it.
		v, _ := ctx.Value(ctxKey("trace")).(string)
		if v != m.traceID {
			t.Errorf("item %d: got trace %q, want %q", m.v, v, m.traceID)
		}
		return m.v * 2, nil
	}, kitsune.WithContextMapper(mapper))

	got := collectAll(t, out)
	sort.Ints(got)
	want := []int{2, 4, 6}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// WithContextMapper — WithName and Buffer option compatibility
// ---------------------------------------------------------------------------

// TestWithContextMapper_WithName verifies that WithName does not interfere
// with WithContextMapper.
func TestWithContextMapper_WithName(t *testing.T) {
	type msg struct {
		v       int
		traceID string
	}
	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}
	p := kitsune.FromSlice([]msg{{1, "s1"}, {2, "s2"}})
	out := kitsune.Map(p, func(ctx context.Context, m msg) (string, error) {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		return v, nil
	}, kitsune.WithContextMapper(mapper), kitsune.WithName("traced-map"))
	got := collectAll(t, out)
	want := []string{"s1", "s2"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestWithContextMapper_Buffer verifies that Buffer(n) does not interfere.
func TestWithContextMapper_Buffer(t *testing.T) {
	type msg struct {
		v       int
		traceID string
	}
	mapper := func(m msg) context.Context {
		return context.WithValue(context.Background(), ctxKey("trace"), m.traceID)
	}
	p := kitsune.FromSlice([]msg{{1, "s1"}, {2, "s2"}, {3, "s3"}})
	out := kitsune.Map(p, func(ctx context.Context, m msg) (string, error) {
		v, _ := ctx.Value(ctxKey("trace")).(string)
		return v, nil
	}, kitsune.WithContextMapper(mapper), kitsune.Buffer(4))
	got := collectAll(t, out)
	want := []string{"s1", "s2", "s3"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
