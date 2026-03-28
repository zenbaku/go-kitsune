package kitsune

// Stage[I, O] is a reusable, composable pipeline transformation.
// A Stage is a first-class value that can be defined, stored, passed as an
// argument, tested in isolation, and combined with other stages via [Then].
//
// Define one with a direct type conversion:
//
//	var ParseStage kitsune.Stage[string, Event] = func(p *kitsune.Pipeline[string]) *kitsune.Pipeline[Event] {
//	    return kitsune.Map(p, parseEvent, kitsune.Concurrency(5))
//	}
//
// Stage[T, T] (type-preserving) has the same signature that [Pipeline.Through] expects,
// so they are directly interchangeable — no adapter needed.
type Stage[I, O any] func(*Pipeline[I]) *Pipeline[O]

// Apply runs this stage against an input pipeline, returning the output pipeline.
func (s Stage[I, O]) Apply(p *Pipeline[I]) *Pipeline[O] {
	return s(p)
}

// Then composes two stages into a single stage that runs first, then second.
//
// Must be a free function: Go methods cannot introduce new type parameters,
// so s1.Then(s2) is not possible when the output type of s1 differs from I.
// For long chains, name intermediate compositions to keep code readable:
//
//	var AB   = kitsune.Then(a, b)
//	var ABCD = kitsune.Then(AB, kitsune.Then(c, d))
func Then[A, B, C any](first Stage[A, B], second Stage[B, C]) Stage[A, C] {
	return func(p *Pipeline[A]) *Pipeline[C] {
		return second(first(p))
	}
}
