package kitsune

import "context"

// Unzip splits a [Pipeline] of [Pair] values into two separate pipelines.
// It is the inverse of [Zip].
//
// Both output pipelines must be consumed — same rule as [Broadcast].
//
//	pairs := kitsune.Zip(as, bs)
//	as2, bs2 := kitsune.Unzip(pairs)
func Unzip[A, B any](p *Pipeline[Pair[A, B]]) (*Pipeline[A], *Pipeline[B]) {
	branches := Broadcast[Pair[A, B]](p, 2)
	as := Map(branches[0], func(_ context.Context, pair Pair[A, B]) (A, error) {
		return pair.First, nil
	})
	bs := Map(branches[1], func(_ context.Context, pair Pair[A, B]) (B, error) {
		return pair.Second, nil
	})
	return as, bs
}
