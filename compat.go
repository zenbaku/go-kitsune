package kitsune

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// ConsecutiveDedup / ConsecutiveDedupBy
// ---------------------------------------------------------------------------

// ConsecutiveDedup drops consecutive duplicate items. Two adjacent items are
// considered duplicates when they are equal (==). Non-consecutive duplicates
// are not affected; use [Dedupe] for global deduplication.
//
//	kitsune.ConsecutiveDedup(kitsune.FromSlice([]int{1, 1, 2, 2, 3}))
//	// emits: 1, 2, 3
func ConsecutiveDedup[T comparable](p *Pipeline[T], opts ...StageOption) *Pipeline[T] {
	var prev T
	hasPrev := false
	return Filter(p, func(_ context.Context, item T) (bool, error) {
		if hasPrev && item == prev {
			return false, nil
		}
		prev = item
		hasPrev = true
		return true, nil
	}, opts...)
}

// ConsecutiveDedupBy drops consecutive items that produce the same key under
// keyFn. Use this when T is not comparable or deduplication should be based on
// a derived field.
//
//	kitsune.ConsecutiveDedupBy(events, func(e Event) string { return e.Type })
func ConsecutiveDedupBy[T any, K comparable](p *Pipeline[T], keyFn func(T) K, opts ...StageOption) *Pipeline[T] {
	var prevKey K
	hasPrev := false
	return Filter(p, func(_ context.Context, item T) (bool, error) {
		k := keyFn(item)
		if hasPrev && k == prevKey {
			return false, nil
		}
		prevKey = k
		hasPrev = true
		return true, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// MapIntersperse
// ---------------------------------------------------------------------------

// MapIntersperse applies fn to each item and inserts sep between consecutive
// mapped outputs (not before the first or after the last).
//
//	kitsune.MapIntersperse(words, ",", strings.ToUpper)
//	// "hello", "world" → "HELLO", ",", "WORLD"
func MapIntersperse[T, O any](p *Pipeline[T], sep O, fn func(context.Context, T) (O, error), opts ...StageOption) *Pipeline[O] {
	first := true
	return FlatMap(p, func(ctx context.Context, item T, yield func(O) error) error {
		out, err := fn(ctx, item)
		if err != nil {
			return err
		}
		if first {
			first = false
			return yield(out)
		}
		if err := yield(sep); err != nil {
			return err
		}
		return yield(out)
	}, opts...)
}

// ---------------------------------------------------------------------------
// CountBy / SumBy
// ---------------------------------------------------------------------------

// aggregateSeq generates unique IDs for CountBy/SumBy stage names.
var aggregateSeq atomic.Int64

// CountBy emits a running snapshot of item counts per key after each input
// item. The key function must return a string. Always runs at Concurrency(1).
// Compose with [Throttle] for periodic snapshots.
//
//	kitsune.CountBy(events, func(e Event) string { return e.Type })
func CountBy[T any](p *Pipeline[T], keyFn func(T) string, opts ...StageOption) *Pipeline[map[string]int64] {
	id := aggregateSeq.Add(1)
	counts := make(map[string]int64)
	return Map(p, func(_ context.Context, item T) (map[string]int64, error) {
		counts[keyFn(item)]++
		snap := make(map[string]int64, len(counts))
		for k, v := range counts {
			snap[k] = v
		}
		return snap, nil
	}, append([]StageOption{Concurrency(1), WithName(fmt.Sprintf("count_by_%d", id))}, opts...)...)
}

// SumBy emits a running snapshot of summed values per key after each input
// item. The key function must return a string. Always runs at Concurrency(1).
// Compose with [Throttle] for periodic snapshots.
//
//	kitsune.SumBy(txns, func(t Txn) string { return t.Account }, func(t Txn) float64 { return t.Amount })
func SumBy[T any, V Numeric](p *Pipeline[T], keyFn func(T) string, valueFn func(T) V, opts ...StageOption) *Pipeline[map[string]V] {
	id := aggregateSeq.Add(1)
	sums := make(map[string]V)
	return Map(p, func(_ context.Context, item T) (map[string]V, error) {
		sums[keyFn(item)] += valueFn(item)
		snap := make(map[string]V, len(sums))
		for k, v := range sums {
			snap[k] = v
		}
		return snap, nil
	}, append([]StageOption{Concurrency(1), WithName(fmt.Sprintf("sum_by_%d", id))}, opts...)...)
}

// ---------------------------------------------------------------------------
// MapBatch
// ---------------------------------------------------------------------------

// MapBatch collects up to size items, passes the slice to fn, and flattens
// the returned slice back into individual items. Useful for bulk DB or API
// calls where batching reduces round-trips.
//
// Use [BatchTimeout] in opts to flush partial batches after a duration.
// Use [Concurrency] to process multiple batches in parallel.
//
//	kitsune.MapBatch(terms, 200, func(ctx context.Context, batch []Term) ([]Enriched, error) {
//	    return db.BulkLookup(ctx, batch)
//	})
func MapBatch[I, O any](p *Pipeline[I], size int, fn func(context.Context, []I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	batched := Batch(p, size, batchCollectOpts(opts)...)
	return FlatMap(batched, func(ctx context.Context, batch []I, yield func(O) error) error {
		results, err := fn(ctx, batch)
		if err != nil {
			return err
		}
		for _, r := range results {
			if err := yield(r); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
}

// batchCollectOpts extracts only the BatchTimeout option for the Batch stage;
// all other options (Concurrency, OnError, etc.) apply to the FlatMap stage.
func batchCollectOpts(opts []StageOption) []StageOption {
	cfg := buildStageConfig(opts)
	if cfg.batchTimeout == 0 {
		return nil
	}
	return []StageOption{BatchTimeout(cfg.batchTimeout)}
}

// ---------------------------------------------------------------------------
// DeadLetter / DeadLetterSink
// ---------------------------------------------------------------------------

// DeadLetter applies fn with optional retry and routes results by outcome:
// successful outputs go to the first (ok) pipeline; items that exhaust all
// retries go to the second (dlq) pipeline as [ErrItem] values.
//
// Unlike [MapResult] which never retries, DeadLetter respects [OnError] with
// [Retry] in opts. Items that succeed on any attempt go to ok; items that
// fail permanently go to dlq.
//
//	ok, dlq := kitsune.DeadLetter(p, fetchUser,
//	    kitsune.OnError(kitsune.Retry(3, kitsune.ExponentialBackoff(10*time.Millisecond, time.Second))),
//	)
//
// Both pipelines must be consumed (same rule as [Partition]).
func DeadLetter[I, O any](p *Pipeline[I], fn func(context.Context, I) (O, error), opts ...StageOption) (*Pipeline[O], *Pipeline[ErrItem[I]]) {
	cfg := buildStageConfig(opts)
	handler := cfg.errorHandler

	// retrying wraps fn with the retry loop from opts. On permanent failure
	// the error propagates to MapResult, which routes it to the dlq port.
	retrying := func(ctx context.Context, item I) (O, error) {
		result, err, _ := internal.ProcessItem(ctx, fn, item, handler)
		return result, err
	}

	return MapResult(p, retrying, deadLetterPassOpts(opts)...)
}

// DeadLetterSink attaches a terminal sink with optional retry and routes
// permanently-failed items to the returned dead-letter pipeline.
// The second return value is a [Runner] that drives the whole graph.
//
//	dlq, runner := kitsune.DeadLetterSink(p, writeToDB,
//	    kitsune.OnError(kitsune.Retry(3, kitsune.FixedBackoff(50*time.Millisecond))),
//	)
//	_ = dlq.ForEach(logFailure).Build()  // must consume dlq
//	runner.Run(ctx)
func DeadLetterSink[I any](p *Pipeline[I], fn func(context.Context, I) error, opts ...StageOption) (*Pipeline[ErrItem[I]], *Runner) {
	cfg := buildStageConfig(opts)
	handler := cfg.errorHandler

	adapted := func(ctx context.Context, item I) (struct{}, error) {
		voidFn := func(ctx context.Context, v I) (struct{}, error) {
			return struct{}{}, fn(ctx, v)
		}
		result, err, _ := internal.ProcessItem(ctx, voidFn, item, handler)
		return result, err
	}

	ok, dlq := MapResult(p, adapted, deadLetterPassOpts(opts)...)
	return dlq, ok.ForEach(func(_ context.Context, _ struct{}) error { return nil }).Build()
}

// deadLetterPassOpts strips OnError options before forwarding to MapResult,
// since MapResult routes all errors to the dlq port and a retry handler there
// would be ignored and misleading.
func deadLetterPassOpts(opts []StageOption) []StageOption {
	pass := make([]StageOption, 0, len(opts))
	for _, o := range opts {
		var c stageConfig
		o(&c)
		if c.errorHandler != nil {
			continue
		}
		pass = append(pass, o)
	}
	return pass
}

// ---------------------------------------------------------------------------
// Stage.Or
// ---------------------------------------------------------------------------

// Or returns a Stage that tries s first and, on error, calls fallback with the
// same input to produce a value. The returned Stage never propagates an error
// from s — fallback is always called on failure.
//
//	fetch := kitsune.Stage[ID, User](func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromDB)
//	})
//	withCache := fetch.Or(func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromCache)
//	})
func (s Stage[I, O]) Or(fallback Stage[I, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		// Run each item through s first; on failure (error or no output) fall back.
		// We use Map with an item-level function that runs sub-pipelines directly.
		return Map(p, func(ctx context.Context, v I) (O, error) {
			result, ok, _ := First(ctx, s(FromSlice([]I{v})))
			if ok {
				return result, nil
			}
			// Primary produced no output (errored or empty) — try fallback.
			result, ok, err := First(ctx, fallback(FromSlice([]I{v})))
			if ok {
				return result, nil
			}
			var zero O
			return zero, err
		})
	}
}

// ---------------------------------------------------------------------------
// WindowByTime
// ---------------------------------------------------------------------------

// WindowByTime collects items into tumbling time windows of the given duration
// and emits each window as a slice when the duration elapses. A partial window
// is emitted when the source completes.
//
// Unlike [Batch] with [BatchTimeout], windows are fixed-duration time buckets:
// a new window always starts at the same interval regardless of item arrival.
//
//	kitsune.WindowByTime(events, time.Second)
//	// emits []Event every second, regardless of how many items arrived
func WindowByTime[T any](p *Pipeline[T], d time.Duration, opts ...StageOption) *Pipeline[[]T] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:     id,
		kind:   "window_by_time",
		name:   orDefault(cfg.name, "window_by_time"),
		buffer: cfg.buffer,
		inputs: []int{p.id},
	}
	build := func(rc *runCtx) chan []T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan []T)
		}
		inCh := p.build(rc)
		ch := make(chan []T, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan(inCh) }()

			outbox := internal.NewBlockingOutbox(ch)
			ticker := time.NewTicker(d)
			defer ticker.Stop()

			var window []T

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						// Source done — flush partial window.
						if len(window) > 0 {
							return outbox.Send(ctx, window)
						}
						return nil
					}
					window = append(window, item)
				case <-ticker.C:
					if len(window) > 0 {
						if err := outbox.Send(ctx, window); err != nil {
							return err
						}
						window = nil
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
