package kitsune

import "sync/atomic"

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
	originalBuild := output.build
	output.build = func(rc *runCtx) chan O {
		// Always overwrite. Build-wrapper composition fires outer first
		// (outer.build runs, then calls originalBuild which is the inner
		// wrapper), so an inner Segment's writes correctly take precedence
		// for IDs in its narrower range. This yields "innermost wins"
		// without explicit guards.
		for _, id := range ids {
			rc.segmentByID[id] = name
		}
		return originalBuild(rc)
	}
	return output
}
