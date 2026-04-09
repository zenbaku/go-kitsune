package kitsune

import "context"

// ---------------------------------------------------------------------------
// Stage — composable pipeline transformer
// ---------------------------------------------------------------------------

// Stage is a composable pipeline transformer: a function that takes an input
// pipeline and returns an output pipeline. Use [Then] to chain stages, and
// [Pipeline.Through] to apply a same-type stage to a pipeline.
//
//	var parseEvent Stage[string, Event] = func(lines *Pipeline[string]) *Pipeline[Event] {
//	    return Map(lines, func(ctx context.Context, line string) (Event, error) {
//	        return json.Unmarshal(line)
//	    })
//	}
//
//	result := parseEvent(lines)
type Stage[I, O any] func(*Pipeline[I]) *Pipeline[O]

// Then chains two stages: the output of s becomes the input of next.
//
//	validate := kitsune.Then(parse, enrich)
//	result := validate(inputPipeline)
func Then[I, M, O any](s Stage[I, M], next Stage[M, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return next(s(p))
	}
}

// Apply runs this stage against p and returns the output pipeline.
// It is equivalent to calling the stage as a function: s(p).
//
//	events, _ := ParseStage.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
func (s Stage[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
	return s(p)
}

// Through applies stage s to the pipeline and returns the result.
//
//	p.Through(normalize).Through(enrich).ForEach(store).Run(ctx)
func (p *Pipeline[T]) Through(s Stage[T, T]) *Pipeline[T] {
	return s(p)
}

// Or returns a Stage that tries primary and, on error, falls back to fallback.
// If primary succeeds its result is emitted; if primary returns an error,
// fallback is called with the same item and its result (or error) is used.
//
//	fetch := kitsune.Or(fetchFromCache, fetchFromDB, kitsune.WithName("fetch"))
func Or[I, O any](primary, fallback func(context.Context, I) (O, error), opts ...StageOption) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return Map(p, func(ctx context.Context, item I) (O, error) {
			result, err := primary(ctx, item)
			if err != nil {
				return fallback(ctx, item)
			}
			return result, nil
		}, opts...)
	}
}
