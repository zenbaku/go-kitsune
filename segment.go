package kitsune

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/zenbaku/go-kitsune/internal"
)

// Segment wraps a [Stage] with a business name. When applied to a pipeline,
// every stage created by the inner Stage is stamped with SegmentName=name in
// its [GraphNode], making the group visible in [Pipeline.Describe] and the
// inspector dashboard.
//
// Segment satisfies [Composable], so it composes with [Then] and
// [Pipeline.Through] interchangeably with [Stage]:
//
//	fetch   := kitsune.NewSegment("fetch-pages",  fetchStage)
//	enrich  := kitsune.NewSegment("enrich-pages", enrichStage)
//	publish := kitsune.NewSegment("publish",      publishStage)
//
//	pipeline := kitsune.Then(kitsune.Then(fetch, enrich), publish)
//
// Nested segments resolve to the innermost name: the deepest enclosing
// Segment owns each stage in the graph.
//
// Two runtime-visible side effects:
//
//   - Segments disable stage fusion at their output boundary, so a
//     fast-path Map/Filter chain inside a Segment does not fuse with a
//     downstream ForEach. This is necessary so the segment's build wrapper
//     (which stamps SegmentName and handles DevStore capture/replay) can
//     intercept the output channel.
//   - When the run is started with [WithDevStore], every named Segment
//     captures its output on first run and replays from snapshot on
//     subsequent runs. See [DevStore] for the dev-iteration semantics.
type Segment[I, O any] struct {
	name  string
	stage Stage[I, O]
	opts  []SegmentOption
}

// SegmentOption is reserved for future per-segment metadata such as
// description, tags, owner, or metric labels. No options are defined in v1.
//
// Unlike [StageOption] and [RunOption] which are function types, SegmentOption
// is an interface with an unexported method. This seals the option surface
// against external implementations until the v1 option set is finalized; once
// the surface stabilizes the type may be switched to a function for symmetry.
type SegmentOption interface {
	applySegment(*segmentConfig)
}

type segmentConfig struct{}

// NewSegment returns a Segment named name that delegates Apply to stage.
// opts is reserved; pass nothing in v1.
func NewSegment[I, O any](name string, stage Stage[I, O], opts ...SegmentOption) Segment[I, O] {
	return Segment[I, O]{name: name, stage: stage, opts: opts}
}

// Apply runs the inner stage against p, then arranges for every newly-created
// stage ID to be registered under the segment name in runCtx.segmentByID at
// run/Describe time. runCtx.add stamps stageMeta.segmentName from that map.
//
// Apply must not be called concurrently with any other pipeline constructor.
// The before/after snapshot of the global stage-ID counter is not a critical
// section: if another goroutine constructs pipeline stages during the inner
// stage's Apply, those stage IDs would be erroneously stamped with this
// segment's name. Pipeline construction is single-goroutine throughout the
// kitsune package; this restriction is consistent with that convention.
func (s Segment[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
	before := atomic.LoadInt64(&globalIDSeq)
	output := s.stage.Apply(p)
	after := atomic.LoadInt64(&globalIDSeq)

	if after <= before {
		return output
	}
	ids := make([]int64, 0, after-before)
	for id := before + 1; id <= after; id++ {
		ids = append(ids, id)
	}

	name := s.name
	// Disable fast-path fusion: fusionEntry bypasses build entirely, which
	// would skip both segment-name stamping and DevStore capture/replay.
	// Clearing it forces ForEach to take the normal build path.
	output.fusionEntry = nil

	originalBuild := output.build
	output.build = func(rc *runCtx) chan O {
		// Always stamp segment IDs first; this is independent of DevStore.
		// Build-wrapper composition fires outer first (outer.build runs,
		// then calls originalBuild which is the inner wrapper), so an inner
		// Segment's writes correctly take precedence for IDs in its
		// narrower range. This yields "innermost wins" without explicit
		// guards.
		for _, id := range ids {
			rc.segmentByID[id] = name
		}

		// DevStore branch: only when a store is attached AND the segment
		// has a non-empty name. Replay if a snapshot exists; capture if
		// not.
		if rc.devStore != nil && name != "" {
			if replayed, ok := tryReplaySegment[O](rc, name, output.id); ok {
				return replayed
			}
			return captureSegment[O](rc, name, originalBuild(rc))
		}
		return originalBuild(rc)
	}
	return output
}

// tryReplaySegment attempts to load a snapshot for name from rc.devStore.
// On success it registers a synthetic stage in rc.stages/rc.metas and
// returns the new stage's output channel and true. The synthetic stage's
// goroutine fires OnStageStart, then per-item OnItem (with zero duration),
// then OnStageDone, so the run is observable via [Hook] and renders as
// a node with Kind="segment-replay" in [GraphHook] payloads.
//
// id is the segment-output Pipeline's id; reusing it keeps downstream
// stage inputs wired correctly (downstream's track(p) recorded inputs
// against this id during construction).
//
// On miss (any error from store.Load) the function returns nil, false;
// the caller falls back to capture mode (run live).
func tryReplaySegment[T any](rc *runCtx, name string, id int64) (chan T, bool) {
	raws, err := rc.devStore.Load(context.Background(), name)
	if err != nil {
		return nil, false
	}

	out := make(chan T, internal.DefaultBuffer)

	fn := func(ctx context.Context) error {
		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		hook.OnStageStart(ctx, name)

		var processed, errs int64
		defer close(out)
		defer func() { hook.OnStageDone(ctx, name, processed, errs) }()

		for _, data := range raws {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			var item T
			if err := json.Unmarshal(data, &item); err != nil {
				errs++
				hook.OnItem(ctx, name, 0, err)
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- item:
				processed++
				hook.OnItem(ctx, name, 0, nil)
			}
		}
		return nil
	}

	meta := stageMeta{
		id:          id,
		name:        name,
		kind:        "segment-replay",
		segmentName: name,
		getChanLen:  func() int { return len(out) },
		getChanCap:  func() int { return cap(out) },
	}
	rc.add(fn, meta)

	return out, true
}

// captureSegment wraps an upstream output channel with a tee goroutine that
// marshals each item into a buffer, forwards items downstream, and calls
// store.Save at end of input.
//
// Like tryReplaySegment, this runs out-of-band of the rc.stages graph.
// Save errors are silently dropped: capture is best-effort and a save
// failure should not escalate into a run failure.
func captureSegment[T any](rc *runCtx, name string, in chan T) chan T {
	out := make(chan T, cap(in))
	go func() {
		defer close(out)
		var buf []json.RawMessage
		for item := range in {
			data, err := json.Marshal(item)
			if err == nil {
				buf = append(buf, data)
			}
			out <- item
		}
		// Save once at end-of-input. Best-effort: errors are dropped.
		_ = rc.devStore.Save(context.Background(), name, buf)
	}()
	return out
}
