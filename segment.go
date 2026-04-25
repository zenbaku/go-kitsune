package kitsune

import (
	"context"
	"encoding/json"
	"sync/atomic"
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
// Segment owns each stage in the graph. Segments are pure metadata and do
// not affect runtime behaviour.
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
			if replayed, ok := tryReplaySegment[O](rc, name); ok {
				return replayed
			}
			return captureSegment[O](rc, name, originalBuild(rc))
		}
		return originalBuild(rc)
	}
	return output
}

// tryReplaySegment attempts to load a snapshot for name from rc.devStore.
// On success it returns a fresh output channel of type T and true; a
// goroutine drains the snapshot into the channel asynchronously.
// On miss (any error from store.Load) it returns nil, false; the caller
// should fall back to capture mode.
//
// This function does not register a stage in rc.stages: the spawned
// goroutine runs out-of-band. That is appropriate because DevStore is a
// dev-only affordance and the goroutine is short-lived (bounded by snapshot
// size, no upstream waiting).
func tryReplaySegment[T any](rc *runCtx, name string) (chan T, bool) {
	raws, err := rc.devStore.Load(context.Background(), name)
	if err != nil {
		// Missing snapshot is the common case; non-missing errors are
		// dev-only and fall through to capture mode (run live).
		return nil, false
	}
	ch := make(chan T, 16)
	go func() {
		defer close(ch)
		for _, data := range raws {
			var item T
			if err := json.Unmarshal(data, &item); err != nil {
				// Best-effort: stop emission, leave channel closed. A
				// future hook could surface this to the user.
				return
			}
			ch <- item
		}
	}()
	return ch, true
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
