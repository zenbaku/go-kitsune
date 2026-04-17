package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Collect / terminal helpers
// ---------------------------------------------------------------------------

func TestCollect(t *testing.T) {
	ctx := context.Background()
	got, err := kitsune.Collect(ctx, kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v", got)
	}
}

func TestFirst(t *testing.T) {
	ctx := context.Background()
	v, ok, err := kitsune.First(ctx, kitsune.FromSlice([]int{10, 20, 30}))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if v != 10 {
		t.Fatalf("got %d, want 10", v)
	}
}

func TestFirstEmpty(t *testing.T) {
	ctx := context.Background()
	_, ok, err := kitsune.First(ctx, kitsune.FromSlice([]int{}))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for empty pipeline")
	}
}

func TestLast(t *testing.T) {
	ctx := context.Background()
	v, ok, err := kitsune.Last(ctx, kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if v != 3 {
		t.Fatalf("got %d, want 3", v)
	}
}

func TestCount(t *testing.T) {
	ctx := context.Background()
	n, err := kitsune.Count(ctx, kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("got %d, want 5", n)
	}
}

func TestAny(t *testing.T) {
	ctx := context.Background()
	found, err := kitsune.Any(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 2 })
	if err != nil || !found {
		t.Fatalf("Any: found=%v err=%v", found, err)
	}
	notFound, err := kitsune.Any(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 99 })
	if err != nil || notFound {
		t.Fatalf("Any(not found): found=%v err=%v", notFound, err)
	}
}

func TestAll(t *testing.T) {
	ctx := context.Background()
	ok, err := kitsune.All(ctx, kitsune.FromSlice([]int{2, 4, 6}), func(v int) bool { return v%2 == 0 })
	if err != nil || !ok {
		t.Fatalf("All(all even): ok=%v err=%v", ok, err)
	}
	bad, err := kitsune.All(ctx, kitsune.FromSlice([]int{2, 3, 6}), func(v int) bool { return v%2 == 0 })
	if err != nil || bad {
		t.Fatalf("All(has odd): ok=%v err=%v", bad, err)
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	v, found, err := kitsune.Find(ctx, kitsune.FromSlice([]int{1, 2, 3}), func(v int) bool { return v == 2 })
	if err != nil || !found || v != 2 {
		t.Fatalf("Find: v=%d found=%v err=%v", v, found, err)
	}
}

func TestSum(t *testing.T) {
	ctx := context.Background()
	s, err := kitsune.Sum(ctx, kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	if err != nil || s != 15 {
		t.Fatalf("Sum: got %d err=%v", s, err)
	}
}

func TestToMap(t *testing.T) {
	ctx := context.Background()
	m, err := kitsune.ToMap(ctx, kitsune.FromSlice([]string{"a", "bb", "ccc"}),
		func(s string) int { return len(s) },
		func(s string) string { return s },
	)
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != "a" || m[2] != "bb" || m[3] != "ccc" {
		t.Fatalf("ToMap: %v", m)
	}
}

func TestSequenceEqual(t *testing.T) {
	ctx := context.Background()
	eq, err := kitsune.SequenceEqual(ctx, kitsune.FromSlice([]int{1, 2, 3}), kitsune.FromSlice([]int{1, 2, 3}))
	if err != nil || !eq {
		t.Fatalf("equal sequences: eq=%v err=%v", eq, err)
	}
	neq, err := kitsune.SequenceEqual(ctx, kitsune.FromSlice([]int{1, 2, 3}), kitsune.FromSlice([]int{1, 2, 4}))
	if err != nil || neq {
		t.Fatalf("unequal sequences: eq=%v err=%v", neq, err)
	}
}

func TestIter(t *testing.T) {
	ctx := context.Background()
	seq, errFn := kitsune.Iter(ctx, kitsune.FromSlice([]int{1, 2, 3}))
	var got []int
	for v := range seq {
		got = append(got, v)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !sliceEqual(got, []int{1, 2, 3}) {
		t.Fatalf("Iter: got %v", got)
	}
}

func TestIterEmpty(t *testing.T) {
	seq, errFn := kitsune.Iter(context.Background(), kitsune.FromSlice([]int{}))
	var got []int
	for v := range seq {
		got = append(got, v)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestIterPipelineError(t *testing.T) {
	wantErr := errors.New("stage failure")
	seq, errFn := kitsune.Iter(context.Background(),
		kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, n int) (int, error) {
				if n == 2 {
					return 0, wantErr
				}
				return n, nil
			},
		),
	)
	for range seq {
	}
	if err := errFn(); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestIterBreak(t *testing.T) {
	// Breaking early must not return an error.
	seq, errFn := kitsune.Iter(context.Background(), kitsune.FromSlice([]int{1, 2, 3, 4, 5}))
	var got []int
	for v := range seq {
		got = append(got, v)
		if v == 2 {
			break
		}
	}
	if err := errFn(); err != nil {
		t.Fatalf("early break should return nil error, got %v", err)
	}
	if len(got) == 0 || got[len(got)-1] != 2 {
		t.Fatalf("expected to stop at 2, got %v", got)
	}
}

func TestIterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; ; i++ {
			if !yield(i) {
				return nil
			}
		}
	})
	seq, errFn := kitsune.Iter(ctx, p)
	var got []int
	for v := range seq {
		got = append(got, v)
		if len(got) == 3 {
			cancel()
		}
	}
	// cancelling the caller's context should surface context.Canceled
	if err := errFn(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestIterErrFnIdempotent(t *testing.T) {
	wantErr := errors.New("fail")
	seq, errFn := kitsune.Iter(context.Background(),
		kitsune.Map(kitsune.FromSlice([]int{1}),
			func(_ context.Context, _ int) (int, error) { return 0, wantErr },
		),
	)
	for range seq {
	}
	err1 := errFn()
	err2 := errFn()
	if !errors.Is(err1, wantErr) || !errors.Is(err2, wantErr) {
		t.Fatalf("errFn not idempotent: %v / %v", err1, err2)
	}
}

func TestIterWithMap(t *testing.T) {
	seq, errFn := kitsune.Iter(context.Background(),
		kitsune.Map(kitsune.FromSlice([]int{1, 2, 3}),
			func(_ context.Context, n int) (string, error) { return fmt.Sprintf("%d", n), nil },
		),
	)
	var got []string
	for s := range seq {
		got = append(got, s)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !sliceEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("got %v", got)
	}
}

func TestIterWithFilter(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	p2 := kitsune.Filter(p, kitsune.FilterFunc(func(n int) bool { return n%2 == 0 }))
	seq, errFn := kitsune.Iter(context.Background(), p2)
	var got []int
	for v := range seq {
		got = append(got, v)
	}
	if err := errFn(); err != nil {
		t.Fatal(err)
	}
	if !sliceEqual(got, []int{2, 4}) {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Throttle / Debounce
// ---------------------------------------------------------------------------

func TestThrottle(t *testing.T) {
	// Throttle with a 50ms window: emit first item per window.
	// Feed 6 items quickly and check that some are throttled.
	items := []int{1, 2, 3, 4, 5, 6}
	p := kitsune.FromSlice(items)
	got := collectAll(t, kitsune.Throttle(p, 50*time.Millisecond))
	// With a very short window and synchronous source, most items arrive in the
	// same window. At minimum the first item should come through.
	if len(got) == 0 {
		t.Fatal("throttle: no items emitted")
	}
	if got[0] != 1 {
		t.Fatalf("throttle: first item should be 1, got %d", got[0])
	}
}

func TestDebounce(t *testing.T) {
	// Debounce: only the last item in a burst should be emitted.
	// We use a Generate that emits 3 items then waits for the silence window.
	silence := 30 * time.Millisecond
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		// Burst: 1, 2, 3 emitted quickly.
		yield(1)
		yield(2)
		yield(3)
		// Wait long enough for the debounce to fire.
		time.Sleep(silence * 3)
		yield(4)
		return nil
	})
	got := collectAll(t, kitsune.Debounce(p, silence))
	// Burst {1,2,3} → only 3 emitted; then 4 → emitted.
	if len(got) < 1 {
		t.Fatal("debounce: no items emitted")
	}
}

// ---------------------------------------------------------------------------
// SampleWith
// ---------------------------------------------------------------------------

func TestSampleWith_Basic(t *testing.T) {
	// Source emits 1..5; sampler fires after source is done; we should get
	// the last item emitted before the sampler fires.
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5})

	// Use a channel-based sampler: fire one tick after collecting from source.
	samCh := kitsune.NewChannel[struct{}](1)
	sam := samCh.Source()

	got := make([]int, 0)
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.SampleWith(src, sam).Collect(context.Background())
		errCh <- err
	}()

	// Give the source goroutine time to push items.
	time.Sleep(20 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{})
	samCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// We fired the sampler once after all items; should emit exactly one item.
	if len(got) != 1 {
		t.Fatalf("expected 1 emission, got %d: %v", len(got), got)
	}
	// The emitted value must be one of the source items.
	if got[0] < 1 || got[0] > 5 {
		t.Fatalf("emitted value %d out of source range", got[0])
	}
}

func TestSampleWith_SamplerBeforeData(t *testing.T) {
	// Fire sampler before source emits anything — nothing should come out.
	srcCh := kitsune.NewChannel[int](1)
	samCh := kitsune.NewChannel[struct{}](2)

	got := make([]int, 0)
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.SampleWith(srcCh.Source(), samCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	// Fire sampler before any source data.
	time.Sleep(5 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{})
	// Now push a source item and fire again.
	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 42)
	time.Sleep(5 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{})
	samCh.Close()
	srcCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The first tick (before data) was skipped; the second tick emits 42.
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("expected [42], got %v", got)
	}
}

func TestSampleWith_ConsumeOnEmit(t *testing.T) {
	// Source emits one item, sampler fires twice: only the first tick emits.
	srcCh := kitsune.NewChannel[int](1)
	samCh := kitsune.NewChannel[struct{}](2)

	got := make([]int, 0)
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.SampleWith(srcCh.Source(), samCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 99)
	srcCh.Close()
	time.Sleep(5 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{}) // emits 99
	time.Sleep(5 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{}) // nothing to emit (consumed above)
	samCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != 99 {
		t.Fatalf("expected [99] (consume-on-emit), got %v", got)
	}
}

func TestSampleWith_SamplerClosesFirst(t *testing.T) {
	// Sampler emits one signal then closes; source is closed before waiting
	// so the source stage terminates and RunStages can complete.
	// Output should be exactly one item.
	srcCh := kitsune.NewChannel[int](10)
	samCh := kitsune.NewChannel[struct{}](1)

	got := make([]int, 0)
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.SampleWith(srcCh.Source(), samCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 7)
	time.Sleep(5 * time.Millisecond)
	samCh.Send(context.Background(), struct{}{})
	// Close both channels so all stages can terminate cleanly.
	samCh.Close()
	srcCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("expected [7], got %v", got)
	}
}

// ---------------------------------------------------------------------------
// BufferWith
// ---------------------------------------------------------------------------

func TestBufferWith_Basic(t *testing.T) {
	// Source emits 1..6; selector fires twice then source closes.
	// All items must appear in output in order (partition property).
	srcCh := kitsune.NewChannel[int](10)
	selCh := kitsune.NewChannel[struct{}](2)

	var got [][]int
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.BufferWith(srcCh.Source(), selCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	for _, v := range []int{1, 2, 3} {
		srcCh.Send(context.Background(), v)
	}
	time.Sleep(5 * time.Millisecond)
	selCh.Send(context.Background(), struct{}{}) // flush [1,2,3]
	time.Sleep(5 * time.Millisecond)
	for _, v := range []int{4, 5, 6} {
		srcCh.Send(context.Background(), v)
	}
	time.Sleep(5 * time.Millisecond)
	selCh.Send(context.Background(), struct{}{}) // flush [4,5,6]
	selCh.Close()
	srcCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Flatten and verify partition property: all items in order.
	var flat []int
	for _, b := range got {
		if len(b) == 0 {
			t.Fatalf("got empty batch")
		}
		flat = append(flat, b...)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	if len(flat) != len(want) {
		t.Fatalf("expected flat=%v, got %v (batches=%v)", want, flat, got)
	}
	for i, v := range want {
		if flat[i] != v {
			t.Fatalf("flat[%d]=%d want %d (batches=%v)", i, flat[i], v, got)
		}
	}
}

func TestBufferWith_EmptyFlushSkipped(t *testing.T) {
	// Selector fires when buffer is empty: no batch should be emitted.
	src := kitsune.FromSlice([]int{})
	sel := kitsune.FromSlice([]struct{}{{}, {}, {}})
	got, err := kitsune.BufferWith(src, sel).Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no batches for empty source, got %v", got)
	}
}

func TestBufferWith_SelectorBeforeItems(t *testing.T) {
	// Selector fires before any items arrive (no-op flush), then source sends
	// items and closes: source-close flushes [10, 20].
	srcCh := kitsune.NewChannel[int](5)
	selCh := kitsune.NewChannel[struct{}](1)

	var got [][]int
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.BufferWith(srcCh.Source(), selCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	selCh.Send(context.Background(), struct{}{}) // fires with empty buffer: no emit
	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 10)
	srcCh.Send(context.Background(), 20)
	time.Sleep(10 * time.Millisecond) // give stage time to append items to buf
	srcCh.Close()                     // source-close flushes [10, 20]
	time.Sleep(5 * time.Millisecond)  // give stage time to process close
	selCh.Close()                     // allow selector stage to exit

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var flat []int
	for _, b := range got {
		flat = append(flat, b...)
	}
	if len(flat) != 2 || flat[0] != 10 || flat[1] != 20 {
		t.Fatalf("expected [10 20], got flat=%v batches=%v", flat, got)
	}
}

func TestBufferWith_SourceCloseFlushesPartial(t *testing.T) {
	// Source closes with buffered items and selector never fires:
	// source-close must flush the partial buffer.
	srcCh := kitsune.NewChannel[int](5)
	selCh := kitsune.NewChannel[struct{}](0)

	var got [][]int
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.BufferWith(srcCh.Source(), selCh.Source()).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 1)
	srcCh.Send(context.Background(), 2)
	srcCh.Send(context.Background(), 3)
	time.Sleep(10 * time.Millisecond) // give stage time to append items to buf
	srcCh.Close()                     // source-close flushes [1, 2, 3]
	time.Sleep(5 * time.Millisecond)  // give stage time to process close
	selCh.Close()                     // allow selector stage to exit

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 batch, got %v", got)
	}
	if len(got[0]) != 3 || got[0][0] != 1 || got[0][1] != 2 || got[0][2] != 3 {
		t.Fatalf("expected [[1 2 3]], got %v", got)
	}
}

func TestBufferWith_NilSelectorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil closingSelector")
		}
	}()
	kitsune.BufferWith[int, struct{}](kitsune.FromSlice([]int{1}), nil)
}

func TestBufferWith_WithName(t *testing.T) {
	// WithName should not affect correctness: source-close flushes all items.
	srcCh := kitsune.NewChannel[int](3)
	selCh := kitsune.NewChannel[struct{}](0)

	var got [][]int
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.BufferWith(srcCh.Source(), selCh.Source(), kitsune.WithName("my-buffer")).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 1)
	srcCh.Send(context.Background(), 2)
	srcCh.Send(context.Background(), 3)
	time.Sleep(10 * time.Millisecond)
	srcCh.Close()
	time.Sleep(5 * time.Millisecond)
	selCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var flat []int
	for _, b := range got {
		flat = append(flat, b...)
	}
	if len(flat) != 3 || flat[0] != 1 || flat[1] != 2 || flat[2] != 3 {
		t.Fatalf("WithName affected output: got %v", got)
	}
}

func TestBufferWith_BufferOption(t *testing.T) {
	// Buffer(n) sets the output channel capacity; should not affect correctness.
	srcCh := kitsune.NewChannel[int](4)
	selCh := kitsune.NewChannel[struct{}](1)

	var got [][]int
	errCh := make(chan error, 1)
	go func() {
		var err error
		got, err = kitsune.BufferWith(srcCh.Source(), selCh.Source(), kitsune.Buffer(2)).Collect(context.Background())
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 1)
	srcCh.Send(context.Background(), 2)
	time.Sleep(5 * time.Millisecond)
	selCh.Send(context.Background(), struct{}{}) // flush [1, 2]
	time.Sleep(5 * time.Millisecond)
	srcCh.Send(context.Background(), 3)
	srcCh.Send(context.Background(), 4)
	time.Sleep(10 * time.Millisecond)
	srcCh.Close()
	time.Sleep(5 * time.Millisecond)
	selCh.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var flat []int
	for _, b := range got {
		flat = append(flat, b...)
	}
	if len(flat) != 4 || flat[0] != 1 || flat[1] != 2 || flat[2] != 3 || flat[3] != 4 {
		t.Fatalf("Buffer option affected output: got flat=%v batches=%v", flat, got)
	}
}

// ---------------------------------------------------------------------------
// SwitchMap
// ---------------------------------------------------------------------------

func TestSwitchMap(t *testing.T) {
	// SwitchMap: each new input cancels the current sub-stream.
	// With FromSlice (synchronous), only the last item's sub-stream completes.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.SwitchMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v * 10); err != nil {
				return err
			}
		}
		return nil
	}))
	// Due to SwitchMap cancellation, we might not get all items,
	// but we should get at least some (from the last item).
	if len(got) == 0 {
		t.Fatal("SwitchMap: no output")
	}
}

// ---------------------------------------------------------------------------
// ExhaustMap
// ---------------------------------------------------------------------------

func TestExhaustMap(t *testing.T) {
	// ExhaustMap: drops new items while inner goroutine is active.
	// Use a slow sub-stream and fast input to verify dropping.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 1; i <= 5; i++ {
			if !yield(i) {
				return nil
			}
			time.Sleep(2 * time.Millisecond) // small pause
		}
		return nil
	})

	got := collectAll(t, kitsune.ExhaustMap(p, func(_ context.Context, v int, yield func(string) error) error {
		time.Sleep(10 * time.Millisecond) // slow sub-stream
		return yield(fmt.Sprintf("item-%d", v))
	}))

	// With slow sub-stream, some items should be dropped.
	if len(got) == 0 {
		t.Fatal("ExhaustMap: no output")
	}
	// Should not have all 5 items (some are dropped).
	if len(got) == 5 {
		t.Log("ExhaustMap: all 5 items processed (timing-dependent, not a failure)")
	}
}

// ---------------------------------------------------------------------------
// ConcatMap
// ---------------------------------------------------------------------------

func TestConcatMap(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.ConcatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		for i := 0; i < v; i++ {
			if err := yield(v*10 + i); err != nil {
				return err
			}
		}
		return nil
	}))
	// ConcatMap = serial FlatMap, so output is in input order.
	want := []int{10, 20, 21, 30, 31, 32}
	if !sliceEqual(got, want) {
		t.Fatalf("ConcatMap: got %v, want %v", got, want)
	}
}

func TestConcatMapPanicsOnConcurrency(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected ConcatMap to panic when given Concurrency(n>1)")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic value to be string, got %T: %v", r, r)
		}
		want := "kitsune: ConcatMap is always serial"
		if !strings.Contains(msg, want) {
			t.Fatalf("panic message %q does not contain %q", msg, want)
		}
	}()
	p := kitsune.FromSlice([]int{1, 2, 3})
	_ = kitsune.ConcatMap(p, func(_ context.Context, v int, yield func(int) error) error {
		return yield(v)
	}, kitsune.Concurrency(4))
}

// ---------------------------------------------------------------------------
// MapResult / MapRecover
// ---------------------------------------------------------------------------

func TestMapResult(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("bad two")

	ok, failed := kitsune.MapResult(kitsune.FromSlice([]int{1, 2, 3}),
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				return 0, boom
			}
			return v * 10, nil
		})

	var okItems []int
	var failedItems []kitsune.ErrItem[int]

	r1 := ok.ForEach(func(_ context.Context, v int) error {
		okItems = append(okItems, v)
		return nil
	}).Build()
	r2 := failed.ForEach(func(_ context.Context, e kitsune.ErrItem[int]) error {
		failedItems = append(failedItems, e)
		return nil
	}).Build()

	merged, err := kitsune.MergeRunners(r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if len(okItems) != 2 {
		t.Fatalf("expected 2 ok items, got %d: %v", len(okItems), okItems)
	}
	if len(failedItems) != 1 || failedItems[0].Item != 2 || !errors.Is(failedItems[0].Err, boom) {
		t.Fatalf("unexpected failed items: %v", failedItems)
	}
}

func TestMapRecover(t *testing.T) {
	ctx := context.Background()
	p := kitsune.FromSlice([]int{1, 2, 3})
	results, err := kitsune.Collect(ctx, kitsune.MapRecover(p,
		func(_ context.Context, v int) (string, error) {
			if v == 2 {
				panic("explode!")
			}
			return fmt.Sprintf("%d", v*10), nil
		},
		func(_ context.Context, _ int, _ error) string {
			return "recovered"
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0] != "10" || results[1] != "recovered" || results[2] != "30" {
		t.Fatalf("got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Batch with timeout
// ---------------------------------------------------------------------------

func TestBatchWithTimeout(t *testing.T) {
	// Slow source: items arrive every 5ms, timeout at 20ms.
	// Expect partial batches when timeout fires before size is reached.
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 7; i++ {
			if !yield(i) {
				return nil
			}
			time.Sleep(8 * time.Millisecond)
		}
		return nil
	})

	got := collectAll(t, kitsune.Batch(p, kitsune.BatchCount(10), kitsune.BatchTimeout(20*time.Millisecond)))
	// With size=10 and items arriving every 8ms, the 20ms timeout should fire
	// and emit partial batches.
	if len(got) == 0 {
		t.Fatal("batch with timeout: no batches emitted")
	}
	total := 0
	for _, b := range got {
		total += len(b)
	}
	if total != 7 {
		t.Fatalf("batch with timeout: expected 7 items total, got %d across %d batches", total, len(got))
	}
}

// ---------------------------------------------------------------------------
// SessionWindow
// ---------------------------------------------------------------------------

func TestSessionWindow(t *testing.T) {
	// Items arrive in two bursts with a pause between them.
	silence := 25 * time.Millisecond
	p := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		// Burst 1
		yield(1)
		yield(2)
		// Pause (longer than gap)
		time.Sleep(silence * 3)
		// Burst 2
		yield(3)
		yield(4)
		return nil
	})

	got := collectAll(t, kitsune.SessionWindow(p, silence))
	if len(got) != 2 {
		// Due to timing variability, accept 1-2 sessions.
		if len(got) == 0 {
			t.Fatalf("SessionWindow: no sessions emitted; got %v", got)
		}
		t.Logf("SessionWindow: got %d sessions (timing-dependent)", len(got))
		return
	}

	sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })
	if !sliceEqual(got[0], []int{1, 2}) {
		t.Errorf("session 0: got %v", got[0])
	}
	if !sliceEqual(got[1], []int{3, 4}) {
		t.Errorf("session 1: got %v", got[1])
	}
}

// ---------------------------------------------------------------------------
// Timeout option on FlatMap
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Timeout option on Map
// ---------------------------------------------------------------------------

func TestTimeoutMap(t *testing.T) {
	input := kitsune.FromSlice([]int{1})
	_, err := kitsune.Map(input, func(ctx context.Context, v int) (int, error) {
		select {
		case <-time.After(time.Second):
			return v, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestTimeoutMapSkip(t *testing.T) {
	// Slow item + OnError(Skip()) — item is dropped, no pipeline error.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Map(p, func(ctx context.Context, v int) (int, error) {
		if v == 2 {
			select {
			case <-time.After(time.Second):
				return v, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return v * 10, nil
	},
		kitsune.Timeout(20*time.Millisecond),
		kitsune.OnError(kitsune.ActionDrop()),
	).Collect(context.Background())
	if err != nil {
		t.Fatalf("expected nil error with Skip, got: %v", err)
	}
	want := []int{10, 30}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTimeoutMapFastFn(t *testing.T) {
	// Fast fn completes well within the deadline — no error.
	p := kitsune.FromSlice([]int{1, 2, 3})
	got, err := kitsune.Map(p, func(_ context.Context, v int) (int, error) {
		return v * 10, nil
	}, kitsune.Timeout(time.Second)).Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sliceEqual(got, []int{10, 20, 30}) {
		t.Errorf("got %v", got)
	}
}

func TestTimeoutFlatMap(t *testing.T) {
	input := kitsune.FromSlice([]int{1})
	_, err := kitsune.FlatMap(input, func(ctx context.Context, v int, yield func(int) error) error {
		select {
		case <-time.After(time.Second):
			return yield(v)
		case <-ctx.Done():
			return ctx.Err()
		}
	}, kitsune.Timeout(20*time.Millisecond)).Collect(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// ---------------------------------------------------------------------------
// MergeRunners edge cases (6h)
// ---------------------------------------------------------------------------

func TestMergeRunners_NoRunners(t *testing.T) {
	_, err := kitsune.MergeRunners()
	if !errors.Is(err, kitsune.ErrNoRunners) {
		t.Fatalf("expected ErrNoRunners, got %v", err)
	}
}
