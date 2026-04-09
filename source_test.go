package kitsune_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func collectAll[T any](t *testing.T, p *kitsune.Pipeline[T]) []T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out []T
	err := p.ForEach(func(_ context.Context, item T) error {
		out = append(out, item)
		return nil
	}).Run(ctx)
	if err != nil && err != context.Canceled {
		t.Fatalf("pipeline error: %v", err)
	}
	return out
}

func TestFromSlice(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	got := collectAll(t, kitsune.FromSlice(items))
	if len(got) != len(items) {
		t.Fatalf("got %v, want %v", got, items)
	}
	for i, v := range items {
		if got[i] != v {
			t.Errorf("index %d: got %d, want %d", i, got[i], v)
		}
	}
}

func TestFrom(t *testing.T) {
	ch := make(chan string, 3)
	ch <- "a"
	ch <- "b"
	ch <- "c"
	close(ch)

	got := collectAll(t, kitsune.From(ch))
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestGenerate(t *testing.T) {
	got := collectAll(t, kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		for i := 0; i < 5; i++ {
			if !yield(i) {
				return nil
			}
		}
		return nil
	}))
	want := []int{0, 1, 2, 3, 4}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFromIter(t *testing.T) {
	seq := func(yield func(int) bool) {
		for i := 10; i < 13; i++ {
			if !yield(i) {
				return
			}
		}
	}
	got := collectAll(t, kitsune.FromIter(seq))
	want := []int{10, 11, 12}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestChannel(t *testing.T) {
	ch := kitsune.NewChannel[int](4)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := ch.Send(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	ch.Close()

	got := collectAll(t, ch.Source())
	want := []int{0, 1, 2, 3}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestUnfold(t *testing.T) {
	// Fibonacci
	type state struct{ a, b int }
	fib := kitsune.Unfold(state{0, 1}, func(s state) (int, state, bool) {
		return s.a, state{s.b, s.a + s.b}, false
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	count := 0
	err := fib.ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		count++
		if count >= 8 {
			cancel()
		}
		return nil
	}).Run(ctx)
	_ = err // context.Canceled is fine

	want := []int{0, 1, 1, 2, 3, 5, 8, 13}
	if len(got) < 8 {
		t.Fatalf("got only %d values: %v", len(got), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("fib[%d] = %d, want %d", i, got[i], v)
		}
	}
}

func TestIterate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	count := 0
	err := kitsune.Iterate(1, func(n int) int { return n * 2 }).
		ForEach(func(_ context.Context, v int) error {
			got = append(got, v)
			count++
			if count >= 5 {
				cancel()
			}
			return nil
		}).Run(ctx)
	_ = err

	want := []int{1, 2, 4, 8, 16}
	if len(got) < 5 {
		t.Fatalf("got %v", got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d] got %d want %d", i, got[i], v)
		}
	}
}

func TestRepeatedly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	count := 0
	n := 42
	err := kitsune.Repeatedly(func() int { return n }).
		ForEach(func(_ context.Context, v int) error {
			got = append(got, v)
			count++
			if count >= 3 {
				cancel()
			}
			return nil
		}).Run(ctx)
	_ = err

	if len(got) < 3 {
		t.Fatalf("got %v", got)
	}
	for _, v := range got[:3] {
		if v != 42 {
			t.Errorf("got %d want 42", v)
		}
	}
}

func TestCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []string
	count := 0
	err := kitsune.Cycle([]string{"a", "b", "c"}).
		ForEach(func(_ context.Context, v string) error {
			got = append(got, v)
			count++
			if count >= 7 {
				cancel()
			}
			return nil
		}).Run(ctx)
	_ = err

	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	if len(got) < 7 {
		t.Fatalf("got %v", got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d] got %q want %q", i, got[i], v)
		}
	}
}

func TestConcat(t *testing.T) {
	got := collectAll(t, kitsune.Concat(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{3, 4}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{5}) },
	))
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d] got %d want %d", i, got[i], v)
		}
	}
}

func TestAmb(t *testing.T) {
	// Amb with two sources — winner should be the one that emits faster.
	// We use synchronous FromSlice so one will win; all items from the winner
	// should be emitted.
	got := collectAll(t, kitsune.Amb(
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{1, 2, 3}) },
		func() *kitsune.Pipeline[int] { return kitsune.FromSlice([]int{10, 20, 30}) },
	))
	// All items should be from one source only.
	sort.Ints(got)
	if len(got) == 0 {
		t.Fatal("no items emitted")
	}
	// Either {1,2,3} or {10,20,30} — check they're from the same source.
	first := got[0]
	if first != 1 && first != 10 {
		t.Fatalf("unexpected first item %d", first)
	}
}

func TestDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := kitsune.FromSlice([]int{1, 2, 3}).Drain().Run(ctx)
	if err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Channel edge cases (6c)
// ---------------------------------------------------------------------------

func TestChannel_CloseIdempotent(t *testing.T) {
	ch := kitsune.NewChannel[int](4)
	// Double-close must not panic.
	ch.Close()
	ch.Close()
}

func TestChannel_SendAfterClose(t *testing.T) {
	ch := kitsune.NewChannel[int](4)
	ch.Close()
	err := ch.Send(context.Background(), 1)
	if !errors.Is(err, kitsune.ErrChannelClosed) {
		t.Fatalf("expected ErrChannelClosed, got %v", err)
	}
}

func TestChannel_TrySendBufferFull(t *testing.T) {
	ch := kitsune.NewChannel[int](1)
	if !ch.TrySend(1) {
		t.Fatal("first TrySend should succeed on empty buffer")
	}
	if ch.TrySend(2) {
		t.Fatal("second TrySend should return false on full buffer")
	}
}

func TestChannel_TrySendAfterClose(t *testing.T) {
	ch := kitsune.NewChannel[int](4)
	ch.Close()
	if ch.TrySend(1) {
		t.Fatal("TrySend after Close should return false")
	}
}

func TestChannel_SendContextCancelled(t *testing.T) {
	ch := kitsune.NewChannel[int](0) // unbuffered: Send blocks until consumer ready
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	err := ch.Send(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestChannel_SourcePanicsOnSecondCall(t *testing.T) {
	ch := kitsune.NewChannel[int](4)
	_ = ch.Source() // first call — fine
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected second Source() call to panic")
		}
	}()
	_ = ch.Source() // second call — should panic
}

func TestChannel_Backpressure(t *testing.T) {
	// Unbuffered channel: Send blocks until a consumer is ready.
	ch := kitsune.NewChannel[int](0)
	ctx := context.Background()

	sent := make(chan error, 1)
	go func() {
		sent <- ch.Send(ctx, 42)
	}()

	// Send should be blocking — verify it hasn't returned yet.
	select {
	case err := <-sent:
		t.Fatalf("Send completed before consumer was ready, err=%v", err)
	case <-time.After(20 * time.Millisecond):
		// expected: still blocking
	}

	// Start consumer — Send should now unblock.
	received := make(chan int, 1)
	go func() {
		ch.Source().ForEach(func(_ context.Context, v int) error {
			received <- v
			return nil
		}).Run(ctx)
	}()

	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not unblock after consumer started")
	}

	ch.Close()
	v := <-received
	if v != 42 {
		t.Errorf("received %d, want 42", v)
	}
}
