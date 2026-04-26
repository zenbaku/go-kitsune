package kitsune_test

import (
	"context"
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
