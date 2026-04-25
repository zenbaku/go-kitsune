package kitsune

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// Stage: composable pipeline transformer
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

// Composable is the unifying interface for things that transform a
// *Pipeline[I] into a *Pipeline[O]. Both [Stage] (a function type with an
// Apply method) and [Segment] (a struct) implement Composable, allowing
// them to compose interchangeably with [Then] and [Pipeline.Through].
type Composable[I, O any] interface {
	Apply(p *Pipeline[I]) *Pipeline[O]
}

// Then chains two composables: the output of first becomes the input of
// second. Both [Stage] and [Segment] satisfy [Composable], so Then accepts
// either interchangeably:
//
//	validate := kitsune.Then(parse, enrich)              // Stage + Stage
//	pipeline := kitsune.Then(fetchSeg, kitsune.Then(enrichSeg, publishSeg)) // nested Segments
//	mixed    := kitsune.Then(parse, enrichSeg)           // Stage + Segment
//
// The returned [Stage] runs first.Apply followed by second.Apply.
func Then[I, M, O any](first Composable[I, M], second Composable[M, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return second.Apply(first.Apply(p))
	}
}

// Apply runs this stage against p and returns the output pipeline.
// It is equivalent to calling the stage as a function: s(p).
//
//	events, _ := ParseStage.Apply(kitsune.FromSlice(testLines)).Collect(ctx)
func (s Stage[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
	return s(p)
}

// Through applies a same-type composable to the pipeline and returns the
// result. Both [Stage] and [Segment] satisfy [Composable]:
//
//	p.Through(normalize).Through(enrich).ForEach(store).Run(ctx)
func (p *Pipeline[T]) Through(s Composable[T, T]) *Pipeline[T] {
	return s.Apply(p)
}

// Or returns a Stage that tries s first and, on error, calls fallback with the
// same input to produce a value.
//
// If both s and fallback return errors, the returned error wraps both via
// [errors.Join] so neither is silently discarded. Callers can inspect either
// cause with [errors.Is] or [errors.As].
//
//	fetch := kitsune.Stage[ID, User](func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromDB)
//	})
//	withCache := fetch.Or(func(p *Pipeline[ID]) *Pipeline[User] {
//	    return kitsune.Map(p, fetchFromCache)
//	})
func (s Stage[I, O]) Or(fallback Stage[I, O]) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return Map(p, func(ctx context.Context, v I) (O, error) {
			results, primaryErr := Collect(ctx, s(FromSlice([]I{v})))
			if primaryErr == nil && len(results) > 0 {
				return results[0], nil
			}
			results, fallbackErr := Collect(ctx, fallback(FromSlice([]I{v})))
			if fallbackErr == nil && len(results) > 0 {
				return results[0], nil
			}
			var zero O
			return zero, errors.Join(primaryErr, fallbackErr)
		})
	}
}

// Or returns a Stage that tries primary and, on error, falls back to fallback.
// If primary succeeds its result is emitted; if primary returns an error,
// fallback is called with the same item and its result (or error) is used.
//
// If both primary and fallback return errors, the returned error wraps both
// via [errors.Join] so neither is silently discarded. Callers can inspect
// either cause with [errors.Is] or [errors.As].
//
//	fetch := kitsune.Or(fetchFromCache, fetchFromDB, kitsune.WithName("fetch"))
func Or[I, O any](primary, fallback func(context.Context, I) (O, error), opts ...StageOption) Stage[I, O] {
	return func(p *Pipeline[I]) *Pipeline[O] {
		return Map(p, func(ctx context.Context, item I) (O, error) {
			result, primaryErr := primary(ctx, item)
			if primaryErr == nil {
				return result, nil
			}
			result, fallbackErr := fallback(ctx, item)
			if fallbackErr == nil {
				return result, nil
			}
			var zero O
			return zero, errors.Join(primaryErr, fallbackErr)
		}, opts...)
	}
}
