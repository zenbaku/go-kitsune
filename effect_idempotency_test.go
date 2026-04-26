package kitsune_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// TestEffect_Idempotency_DefaultStoreDedupesWithinRun exercises the
// happy path: a source emits duplicate items; with Idempotent: true and
// an IdempotencyKey set, the effect function is called once per unique
// key, and duplicates are emitted as Deduped outcomes.
func TestEffect_Idempotency_DefaultStoreDedupesWithinRun(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v * 10, nil
	}

	src := kitsune.FromSlice([]int{1, 2, 1, 3, 2})
	policy := kitsune.EffectPolicy{
		Required:       true,
		Idempotent:     true,
		IdempotencyKey: func(item any) string { return intKey(item) },
	}

	var outcomes []kitsune.EffectOutcome[int, int]
	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("dedupe-effect"))).
		ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
			outcomes = append(outcomes, o)
			return nil
		}).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 3 {
		t.Errorf("fnCalls=%d, want 3 (one per unique key 1,2,3)", got)
	}
	if len(outcomes) != 5 {
		t.Fatalf("len(outcomes)=%d, want 5", len(outcomes))
	}

	// Outcomes 0,1,3 (inputs 1,2,3) are first occurrences and should run fn.
	for _, idx := range []int{0, 1, 3} {
		if outcomes[idx].Deduped {
			t.Errorf("outcomes[%d] (input=%d) Deduped=true, want false", idx, outcomes[idx].Input)
		}
		if !outcomes[idx].Applied {
			t.Errorf("outcomes[%d] (input=%d) Applied=false, want true", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Result != outcomes[idx].Input*10 {
			t.Errorf("outcomes[%d] Result=%d, want %d", idx, outcomes[idx].Result, outcomes[idx].Input*10)
		}
	}

	// Outcomes 2,4 (inputs 1,2 again) are duplicates and should be deduped.
	for _, idx := range []int{2, 4} {
		if !outcomes[idx].Deduped {
			t.Errorf("outcomes[%d] (input=%d) Deduped=false, want true", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Applied {
			t.Errorf("outcomes[%d] (input=%d) Applied=true, want false", idx, outcomes[idx].Input)
		}
		if outcomes[idx].Result != 0 {
			t.Errorf("outcomes[%d] Result=%d, want zero (deduped)", idx, outcomes[idx].Result)
		}
	}

	stats := summary.EffectStats["dedupe-effect"]
	if stats.Success != 3 {
		t.Errorf("EffectStats.Success=%d, want 3", stats.Success)
	}
	if stats.Failure != 0 {
		t.Errorf("EffectStats.Failure=%d, want 0", stats.Failure)
	}
	if stats.Deduped != 2 {
		t.Errorf("EffectStats.Deduped=%d, want 2", stats.Deduped)
	}
}

// intKey returns the input cast to int and stringified, or "" if the
// item is not an int. Shared across the dedupe test set.
func intKey(item any) string {
	v, ok := item.(int)
	if !ok {
		return ""
	}
	return itoa(v)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestEffect_Idempotency_EmptyKeyOptsOut verifies that returning "" from
// IdempotencyKey opts an item out of dedupe: the effect function runs
// normally and the item is NOT recorded in the store.
func TestEffect_Idempotency_EmptyKeyOptsOut(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	src := kitsune.FromSlice([]int{1, 1, 1, 2, 2})
	policy := kitsune.EffectPolicy{
		Required:   true,
		Idempotent: true,
		IdempotencyKey: func(item any) string {
			v, _ := item.(int)
			if v == 1 {
				return "" // opt out for input value 1
			}
			return intKey(item)
		},
	}

	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("opt-out"))).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Three 1s (all opt out, all run) + first 2 (runs) = 4 fn calls. Second 2 is deduped.
	if got := fnCalls.Load(); got != 4 {
		t.Errorf("fnCalls=%d, want 4", got)
	}
	stats := summary.EffectStats["opt-out"]
	if stats.Deduped != 1 {
		t.Errorf("Deduped=%d, want 1", stats.Deduped)
	}
}

// countingStore is a fake IdempotencyStore that wraps an inner map and
// counts Add calls; used to verify a user-supplied store is honoured.
type countingStore struct {
	calls atomic.Int64
	mu    sync.Mutex
	seen  map[string]struct{}
}

func newCountingStore() *countingStore {
	return &countingStore{seen: make(map[string]struct{})}
}

func (c *countingStore) Add(_ context.Context, key string) (bool, error) {
	c.calls.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[key]; ok {
		return false, nil
	}
	c.seen[key] = struct{}{}
	return true, nil
}

// TestEffect_Idempotency_UserSuppliedStoreIsUsed verifies that an
// IdempotencyStore set on the EffectPolicy replaces the default in-memory
// store: every dedupe-eligible item triggers a call on the user store.
func TestEffect_Idempotency_UserSuppliedStoreIsUsed(t *testing.T) {
	ctx := context.Background()

	store := newCountingStore()
	fn := func(_ context.Context, v int) (int, error) { return v, nil }
	src := kitsune.FromSlice([]int{1, 2, 3, 1})
	policy := kitsune.EffectPolicy{
		Required:         true,
		Idempotent:       true,
		IdempotencyKey:   intKey,
		IdempotencyStore: store,
	}

	if _, err := kitsune.Effect(src, fn, policy).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := store.calls.Load(); got != 4 {
		t.Errorf("store.calls=%d, want 4 (one Add per item)", got)
	}
}

// erroringStore returns an error from every Add. Used to verify that
// IdempotencyStore failures surface as per-item failures and the effect
// function is NOT invoked.
type erroringStore struct{ err error }

func (e *erroringStore) Add(_ context.Context, _ string) (bool, error) {
	return false, e.err
}

// TestEffect_Idempotency_StoreErrorIsPerItemFailure verifies that an
// IdempotencyStore.Add error is recorded as a per-item failure: the
// outcome carries Err, the failure counter increments, and the effect
// function is not called.
func TestEffect_Idempotency_StoreErrorIsPerItemFailure(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	storeErr := errors.New("store-down")
	src := kitsune.FromSlice([]int{1, 2})
	policy := kitsune.EffectPolicy{
		Required:         true,
		Idempotent:       true,
		IdempotencyKey:   intKey,
		IdempotencyStore: &erroringStore{err: storeErr},
	}

	var outcomes []kitsune.EffectOutcome[int, int]
	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("erroring"))).
		ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, int]) error {
			outcomes = append(outcomes, o)
			return nil
		}).Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 0 {
		t.Errorf("fnCalls=%d, want 0 (fn must not run when store errors)", got)
	}
	if len(outcomes) != 2 {
		t.Fatalf("len(outcomes)=%d, want 2", len(outcomes))
	}
	for i, o := range outcomes {
		if !errors.Is(o.Err, storeErr) {
			t.Errorf("outcomes[%d].Err=%v, want %v", i, o.Err, storeErr)
		}
		if o.Deduped {
			t.Errorf("outcomes[%d].Deduped=true, want false (store-error path)", i)
		}
	}
	stats := summary.EffectStats["erroring"]
	if stats.Failure != 2 {
		t.Errorf("Failure=%d, want 2", stats.Failure)
	}
	if stats.Deduped != 0 {
		t.Errorf("Deduped=%d, want 0", stats.Deduped)
	}
}

// TestEffect_Idempotency_ConcurrentDedupeIsRaceFree verifies that under
// Concurrency(n), two workers seeing the same key result in exactly one
// fn call. The IdempotencyStore.Add atomic contract carries the load.
func TestEffect_Idempotency_ConcurrentDedupeIsRaceFree(t *testing.T) {
	ctx := context.Background()

	var fnCalls atomic.Int64
	fn := func(_ context.Context, v int) (int, error) {
		fnCalls.Add(1)
		return v, nil
	}

	const N = 50
	items := make([]int, N)
	for i := range items {
		items[i] = 1 // all the same key
	}
	src := kitsune.FromSlice(items)
	policy := kitsune.EffectPolicy{
		Required:       true,
		Idempotent:     true,
		IdempotencyKey: intKey,
	}

	summary, err := kitsune.Effect(src, fn, policy,
		kitsune.EffectStageOption(kitsune.WithName("concurrent")),
		kitsune.EffectStageOption(kitsune.Concurrency(4))).
		ForEach(func(_ context.Context, _ kitsune.EffectOutcome[int, int]) error { return nil }).
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fnCalls.Load(); got != 1 {
		t.Errorf("fnCalls=%d, want 1 (exactly one worker should win the race)", got)
	}
	stats := summary.EffectStats["concurrent"]
	if stats.Success != 1 {
		t.Errorf("Success=%d, want 1", stats.Success)
	}
	if stats.Deduped != N-1 {
		t.Errorf("Deduped=%d, want %d", stats.Deduped, N-1)
	}
}
