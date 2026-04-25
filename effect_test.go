package kitsune_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenbaku/go-kitsune"
)

// TestEffect_Success verifies that with no errors and no retries configured,
// every input produces an outcome with Applied: true and the expected result.
func TestEffect_Success(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	fn := func(_ context.Context, v int) (string, error) {
		return fmt.Sprintf("v=%d", v), nil
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(out))
	}
	for i, o := range out {
		if !o.Applied {
			t.Errorf("outcome %d: Applied=false, want true", i)
		}
		if o.Err != nil {
			t.Errorf("outcome %d: err=%v, want nil", i, o.Err)
		}
		if o.Result != fmt.Sprintf("v=%d", o.Input) {
			t.Errorf("outcome %d: result=%q, want %q", i, o.Result, fmt.Sprintf("v=%d", o.Input))
		}
	}
}

// TestEffect_RetryThenSuccess verifies that with RetryUpTo(3), a function
// that fails twice and then succeeds produces an outcome with Applied:true.
func TestEffect_RetryThenSuccess(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{42})
	var attempts atomic.Int32
	fn := func(_ context.Context, v int) (string, error) {
		n := attempts.Add(1)
		if n < 3 {
			return "", errors.New("transient")
		}
		return "ok", nil
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
		kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(0))},
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || !out[0].Applied || out[0].Result != "ok" {
		t.Fatalf("got %+v, want one applied outcome with result=ok", out)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts=%d, want 3", attempts.Load())
	}
}

// TestEffect_RetryExhaustion verifies that after Retry.MaxAttempts attempts
// have all failed, the outcome reports Applied:false and carries the last
// error.
func TestEffect_RetryExhaustion(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	sentinel := errors.New("permanent")
	var attempts atomic.Int32
	fn := func(_ context.Context, _ int) (struct{}, error) {
		attempts.Add(1)
		return struct{}{}, sentinel
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
		kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(3, kitsune.FixedBackoff(0))},
	))
	if err != nil {
		t.Fatalf("unexpected pipeline error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(out))
	}
	if out[0].Applied {
		t.Errorf("Applied=true, want false")
	}
	if !errors.Is(out[0].Err, sentinel) {
		t.Errorf("err=%v, want sentinel", out[0].Err)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts=%d, want 3", attempts.Load())
	}
}

// TestEffect_AttemptTimeout verifies that AttemptTimeout cancels a slow
// attempt and that the cancellation is treated as a retryable error.
func TestEffect_AttemptTimeout(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	var attempts atomic.Int32
	fn := func(ctx context.Context, _ int) (int, error) {
		n := attempts.Add(1)
		if n == 1 {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return 7, nil
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
		kitsune.AttemptTimeout(20*time.Millisecond),
		kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(2, kitsune.FixedBackoff(0))},
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || !out[0].Applied || out[0].Result != 7 {
		t.Fatalf("got %+v, want one applied outcome with result=7", out)
	}
}

// TestEffect_DryRun verifies that under DryRun(), fn is never called and
// every outcome reports Applied:false with no error.
func TestEffect_DryRun(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1, 2, 3})
	var calls atomic.Int32
	fn := func(_ context.Context, _ int) (string, error) {
		calls.Add(1)
		return "should not run", nil
	}
	p := kitsune.Effect(src, fn)

	var got []kitsune.EffectOutcome[int, string]
	var mu sync.Mutex
	runner := p.ForEach(func(_ context.Context, o kitsune.EffectOutcome[int, string]) error {
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
		return nil
	})

	if err := runner.Run(ctx, kitsune.DryRun()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("fn called %d times, want 0", calls.Load())
	}
	if len(got) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(got))
	}
	for i, o := range got {
		if o.Applied {
			t.Errorf("outcome %d: Applied=true, want false in dry-run mode", i)
		}
		if o.Err != nil {
			t.Errorf("outcome %d: err=%v, want nil in dry-run mode", i, o.Err)
		}
	}
}

// TestEffect_GraphMetadata verifies that an Effect stage is reported with
// IsEffect=true and EffectRequired matching the configured policy.
func TestEffect_GraphMetadata(t *testing.T) {
	src := kitsune.FromSlice([]int{1})
	fn := func(_ context.Context, v int) (int, error) { return v, nil }

	requiredP := kitsune.Effect(src, fn) // default Required
	bestEffortP := kitsune.Effect(src, fn, kitsune.BestEffort())

	cases := []struct {
		name    string
		nodes   []kitsune.GraphNode
		wantReq bool
	}{
		{"required", requiredP.Describe(), true},
		{"best-effort", bestEffortP.Describe(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var found bool
			for _, n := range c.nodes {
				if n.Kind == "effect" {
					found = true
					if !n.IsEffect {
						t.Errorf("IsEffect=false, want true")
					}
					if n.EffectRequired != c.wantReq {
						t.Errorf("EffectRequired=%v, want %v", n.EffectRequired, c.wantReq)
					}
				}
			}
			if !found {
				t.Errorf("no effect node found in graph")
			}
		})
	}
}

// TestEffect_PolicyOverride verifies that a per-call option after an
// EffectPolicy overwrites the policy's value (last-write-wins).
func TestEffect_PolicyOverride(t *testing.T) {
	src := kitsune.FromSlice([]int{1})
	fn := func(_ context.Context, v int) (int, error) { return v, nil }

	pol := kitsune.EffectPolicy{Required: false}
	p := kitsune.Effect(src, fn, pol, kitsune.Required())

	for _, n := range p.Describe() {
		if n.Kind == "effect" && !n.EffectRequired {
			t.Errorf("EffectRequired=false; expected per-call Required() to override policy")
		}
	}
}

// TestEffect_StageOptionsApply verifies that EffectStageOption(WithName) and
// EffectStageOption(Buffer) are honoured by the underlying stage.
func TestEffect_StageOptionsApply(t *testing.T) {
	src := kitsune.FromSlice([]int{1, 2, 3})
	fn := func(_ context.Context, v int) (int, error) { return v, nil }

	p := kitsune.Effect(src, fn,
		kitsune.EffectStageOption(kitsune.WithName("publish")),
		kitsune.EffectStageOption(kitsune.Buffer(64)),
	)

	var got kitsune.GraphNode
	for _, n := range p.Describe() {
		if n.Kind == "effect" {
			got = n
		}
	}
	if got.Name != "publish" {
		t.Errorf("Name=%q, want %q", got.Name, "publish")
	}
	if got.Buffer != 64 {
		t.Errorf("Buffer=%d, want 64", got.Buffer)
	}
}

// TestEffect_NoRetryByDefault verifies that a bare Effect with no
// EffectPolicy makes exactly one attempt — the zero value of RetryStrategy
// must not silently retry forever. This is a regression guard for the
// Effect-specific MaxAttempts semantics: 0 means single attempt.
func TestEffect_NoRetryByDefault(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	sentinel := errors.New("permanent")
	var attempts atomic.Int32
	fn := func(_ context.Context, _ int) (int, error) {
		attempts.Add(1)
		return 0, sentinel
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn))
	if err != nil {
		t.Fatalf("unexpected pipeline error: %v", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts=%d, want 1 (zero-value RetryStrategy must mean single attempt)", attempts.Load())
	}
	if len(out) != 1 || out[0].Applied {
		t.Fatalf("got %+v, want one terminal-failure outcome", out)
	}
	if !errors.Is(out[0].Err, sentinel) {
		t.Errorf("err=%v, want sentinel", out[0].Err)
	}
	if out[0].Result != 0 {
		t.Errorf("Result=%d, want 0 (zero value of int) on terminal failure", out[0].Result)
	}
}

// TestEffect_FailureReturnsZeroResult verifies the godoc contract: on
// terminal failure, the outcome carries the zero value of R, not the last
// attempt's partial result. Regression guard for issue surfaced in code
// review of a5277b5.
func TestEffect_FailureReturnsZeroResult(t *testing.T) {
	ctx := context.Background()
	src := kitsune.FromSlice([]int{1})
	sentinel := errors.New("permanent")
	fn := func(_ context.Context, _ int) (string, error) {
		return "non-zero-leaked", sentinel
	}
	out, err := kitsune.Collect(ctx, kitsune.Effect(src, fn,
		kitsune.EffectPolicy{Retry: kitsune.RetryUpTo(2, kitsune.FixedBackoff(0))},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Applied {
		t.Fatalf("got %+v, want one terminal-failure outcome", out)
	}
	if out[0].Result != "" {
		t.Errorf("Result=%q, want \"\" (zero value of string)", out[0].Result)
	}
}
