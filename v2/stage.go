package kitsune

import "context"

// ---------------------------------------------------------------------------
// Stage — composable, named processing function
// ---------------------------------------------------------------------------

// Stage is a named, composable processing function compatible with [Map].
// It is a plain function type with zero runtime overhead.
//
//	var parseEvent Stage[string, Event] = func(ctx context.Context, line string) (Event, error) {
//	    return json.Unmarshal(line)
//	}
//
//	kitsune.Map(lines, parseEvent)
type Stage[I, O any] func(context.Context, I) (O, error)

// Then chains two stages: the output of s becomes the input of next.
// The resulting stage fails fast — if s returns an error, next is not called.
//
//	validate := kitsune.Then(parse, enrich)
//	kitsune.Map(p, validate)
func Then[I, M, O any](s Stage[I, M], next Stage[M, O]) Stage[I, O] {
	return func(ctx context.Context, v I) (O, error) {
		m, err := s(ctx, v)
		if err != nil {
			var zero O
			return zero, err
		}
		return next(ctx, m)
	}
}

// Through pipes every item in the pipeline through s and returns the transformed pipeline.
// It is equivalent to kitsune.Map(p, s, opts...).
//
//	p.Through(normalize).Through(enrich).ForEach(store).Run(ctx)
func (p *Pipeline[T]) Through(s Stage[T, T], opts ...StageOption) *Pipeline[T] {
	return Map(p, s, opts...)
}
