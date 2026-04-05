package kitsune

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

// Through applies stage s to the pipeline and returns the result.
//
//	p.Through(normalize).Through(enrich).ForEach(store).Run(ctx)
func (p *Pipeline[T]) Through(s Stage[T, T]) *Pipeline[T] {
	return s(p)
}
