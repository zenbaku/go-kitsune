package kitsune

import (
	"context"
	"encoding/json"
	"fmt"
)

// FromCheckpoint creates a [Pipeline] that emits items previously persisted
// under segment in store. It is the test-time companion to [WithDevStore]:
// load a stored snapshot directly as a source, without re-running the
// pipeline that produced it.
//
// Items are unmarshaled using encoding/json. The element type T must be
// JSON-compatible with whatever was stored. If the snapshot is missing,
// Run returns an error wrapping [ErrSnapshotMissing]. Unmarshal errors
// abort the source.
//
//	store := kitsune.NewFileDevStore("testdata/checkpoints")
//	p := kitsune.FromCheckpoint[EnrichedPage](store, "enrich-pages")
//	// Use p as the input to a downstream stage under test.
//
// Stage options like [WithName] and [Buffer] are honoured; concurrency
// options have no effect on a source.
func FromCheckpoint[T any](store DevStore, segment string, opts ...StageOption) *Pipeline[T] {
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "source",
		name:        orDefault(cfg.name, "from_checkpoint"),
		concurrency: 1,
		buffer:      cfg.buffer,
	}
	var out *Pipeline[T]
	build := func(rc *runCtx) chan T {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan T)
		}
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan T, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)
		rc.initDrainNotify(id, out.consumerCount.Load())
		drainCh := rc.drainCh(id)
		gate := rc.gate

		stage := sourceStage(ch, gate, drainCh, func(ctx context.Context, send func(T) error) error {
			raw, err := store.Load(ctx, segment)
			if err != nil {
				return fmt.Errorf("kitsune: from_checkpoint %q: %w", segment, err)
			}
			for i, data := range raw {
				var item T
				if err := json.Unmarshal(data, &item); err != nil {
					return fmt.Errorf("kitsune: from_checkpoint %q item %d: %w", segment, i, err)
				}
				if err := send(item); err != nil {
					return err
				}
			}
			return nil
		})

		rc.add(stage, m)
		return ch
	}
	out = newPipeline(id, meta, build)
	return out
}
