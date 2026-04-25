package kitsune_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Notification type tests
// ---------------------------------------------------------------------------

func TestNotification_Constructors(t *testing.T) {
	next := kitsune.NextNotification(42)
	if next.Value != 42 || next.Err != nil || next.Done {
		t.Fatalf("NextNotification: got %+v", next)
	}

	sentinel := errors.New("sentinel")
	errN := kitsune.ErrorNotification[int](sentinel)
	if errN.Err != sentinel || !errN.Done {
		t.Fatalf("ErrorNotification: got %+v", errN)
	}

	done := kitsune.CompleteNotification[int]()
	if done.Err != nil || !done.Done {
		t.Fatalf("CompleteNotification: got %+v", done)
	}
}

func TestNotification_Predicates(t *testing.T) {
	next := kitsune.NextNotification(1)
	if !next.IsValue() || next.IsError() || next.IsComplete() {
		t.Errorf("NextNotification predicates wrong: %+v", next)
	}

	sentinel := errors.New("oops")
	errN := kitsune.ErrorNotification[int](sentinel)
	if errN.IsValue() || !errN.IsError() || errN.IsComplete() {
		t.Errorf("ErrorNotification predicates wrong: %+v", errN)
	}

	done := kitsune.CompleteNotification[int]()
	if done.IsValue() || done.IsError() || !done.IsComplete() {
		t.Errorf("CompleteNotification predicates wrong: %+v", done)
	}
}

func TestNotification_ZeroValue(t *testing.T) {
	// The zero value is a valid value notification (IsValue == true).
	var n kitsune.Notification[int]
	if !n.IsValue() {
		t.Errorf("zero Notification[int] should satisfy IsValue, got %+v", n)
	}
}

// ---------------------------------------------------------------------------
// Materialize tests
// ---------------------------------------------------------------------------

func TestMaterialize_Values(t *testing.T) {
	p := kitsune.FromSlice([]int{1, 2, 3})
	got := collectAll(t, kitsune.Materialize(p))

	// Expect: Next(1), Next(2), Next(3), Complete.
	if len(got) != 4 {
		t.Fatalf("expected 4 notifications, got %d: %v", len(got), got)
	}
	for i, n := range got[:3] {
		if !n.IsValue() || n.Value != i+1 {
			t.Errorf("notification %d: want NextNotification(%d), got %+v", i, i+1, n)
		}
	}
	if !got[3].IsComplete() {
		t.Errorf("last notification: want CompleteNotification, got %+v", got[3])
	}
}

func TestMaterialize_EmptyUpstream(t *testing.T) {
	got := collectAll(t, kitsune.Materialize(kitsune.Empty[int]()))

	// Expect: just the Complete notification.
	if len(got) != 1 || !got[0].IsComplete() {
		t.Fatalf("expected exactly one CompleteNotification, got %v", got)
	}
}

func TestMaterialize_ErrorBecomesNotification(t *testing.T) {
	sentinel := errors.New("boom")
	// Source that emits 2 items then errors.
	src := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(10)
		yield(20)
		return sentinel
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []kitsune.Notification[int]
	// collectAll fatals on non-context errors, so use ForEach directly.
	_, err := kitsune.Materialize(src).ForEach(func(_ context.Context, n kitsune.Notification[int]) error {
		got = append(got, n)
		return nil
	}).Run(ctx)

	// Materialize absorbs the error; Run should return nil.
	if err != nil {
		t.Fatalf("expected nil from Run, got %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 notifications (2 values + 1 error), got %d: %v", len(got), got)
	}
	if !got[0].IsValue() || got[0].Value != 10 {
		t.Errorf("notification 0: want Next(10), got %+v", got[0])
	}
	if !got[1].IsValue() || got[1].Value != 20 {
		t.Errorf("notification 1: want Next(20), got %+v", got[1])
	}
	if !got[2].IsError() || !errors.Is(got[2].Err, sentinel) {
		t.Errorf("notification 2: want ErrorNotification(sentinel), got %+v", got[2])
	}
}

func TestMaterialize_ContextCancelNotMaterialized(t *testing.T) {
	// A source that never ends; cancel the context mid-run.
	src := kitsune.Never[int]()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var got []kitsune.Notification[int]
	_, err := kitsune.Materialize(src).ForEach(func(_ context.Context, n kitsune.Notification[int]) error {
		got = append(got, n)
		return nil
	}).Run(ctx)

	// Context cancellation propagates as an error from Run, not as a notification.
	if err == nil {
		t.Fatal("expected context error from Run, got nil")
	}
	// No notifications should have been emitted (or at least no error notification).
	for _, n := range got {
		if n.IsError() {
			t.Errorf("context cancellation must not produce an error notification, got %+v", n)
		}
	}
}

// ---------------------------------------------------------------------------
// Dematerialize tests
// ---------------------------------------------------------------------------

func TestDematerialize_Values(t *testing.T) {
	notifications := []kitsune.Notification[int]{
		kitsune.NextNotification(1),
		kitsune.NextNotification(2),
		kitsune.NextNotification(3),
		kitsune.CompleteNotification[int](),
	}
	p := kitsune.FromSlice(notifications)
	got := collectAll(t, kitsune.Dematerialize(p))

	want := []int{1, 2, 3}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDematerialize_ErrorNotificationPropagates(t *testing.T) {
	sentinel := errors.New("re-injected")
	notifications := []kitsune.Notification[int]{
		kitsune.NextNotification(1),
		kitsune.ErrorNotification[int](sentinel),
	}
	p := kitsune.FromSlice(notifications)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	_, err := kitsune.Dematerialize(p).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(ctx)

	if err == nil {
		t.Fatal("expected error from Run, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error to wrap sentinel, got %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("expected [1] before error, got %v", got)
	}
}

func TestDematerialize_StopsAfterComplete(t *testing.T) {
	// Items after the Complete notification should be ignored.
	notifications := []kitsune.Notification[int]{
		kitsune.NextNotification(1),
		kitsune.CompleteNotification[int](),
		kitsune.NextNotification(99), // should not be emitted
	}
	p := kitsune.FromSlice(notifications)
	got := collectAll(t, kitsune.Dematerialize(p))

	want := []int{1}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDematerialize_UpstreamClosedWithoutTerminal(t *testing.T) {
	// No Done notification; upstream just closes. Should complete normally.
	notifications := []kitsune.Notification[int]{
		kitsune.NextNotification(7),
		kitsune.NextNotification(8),
	}
	p := kitsune.FromSlice(notifications)
	got := collectAll(t, kitsune.Dematerialize(p))

	want := []int{7, 8}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDematerialize_MalformedMidStreamError(t *testing.T) {
	// Done=false, Err!=nil: malformed but should still propagate as an error.
	sentinel := errors.New("mid-stream")
	notifications := []kitsune.Notification[int]{
		{Value: 5},
		{Err: sentinel, Done: false}, // malformed
	}
	p := kitsune.FromSlice(notifications)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := kitsune.Dematerialize(p).ForEach(func(_ context.Context, _ int) error {
		return nil
	}).Run(ctx)

	if err == nil {
		t.Fatal("expected error from malformed notification, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error to wrap sentinel, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Roundtrip tests
// ---------------------------------------------------------------------------

func TestMaterializeDematerialize_Identity_Values(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3, 4, 5})
	roundtripped := kitsune.Dematerialize(kitsune.Materialize(src))
	got := collectAll(t, roundtripped)
	want := []int{1, 2, 3, 4, 5}
	if !sliceEqual(got, want) {
		t.Fatalf("roundtrip: got %v, want %v", got, want)
	}
}

func TestMaterializeDematerialize_Identity_Error(t *testing.T) {
	sentinel := errors.New("upstream-error")
	src := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		yield(2)
		return sentinel
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	_, err := kitsune.Dematerialize(kitsune.Materialize(src)).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(ctx)

	// Error should be re-injected by Dematerialize.
	if err == nil {
		t.Fatal("expected error from Run, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("expected values %v before error, got %v", want, got)
	}
}

func TestMaterialize_ComposableMap(t *testing.T) {
	// Demonstrate the key use-case: route errors through a Map that only handles T.
	// The Map tags each notification as "ok" or "error", then Dematerialize
	// re-injects the error so downstream behaves normally.
	sentinel := errors.New("upstream-fail")
	src := kitsune.Generate(func(ctx context.Context, yield func(int) bool) error {
		yield(1)
		yield(2)
		return sentinel
	})

	// Materialize, tag with a string via Map, then Dematerialize.
	tagged := kitsune.Map(
		kitsune.Materialize(src),
		func(_ context.Context, n kitsune.Notification[int]) (kitsune.Notification[int], error) {
			// Pass notifications through unchanged; this demonstrates that a Map
			// stage can inspect or transform notifications without needing to
			// handle errors as a special case in the pipeline.
			return n, nil
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got []int
	_, err := kitsune.Dematerialize(tagged).ForEach(func(_ context.Context, v int) error {
		got = append(got, v)
		return nil
	}).Run(ctx)

	if err == nil {
		t.Fatal("expected error after Dematerialize, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	want := []int{1, 2}
	if !sliceEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
