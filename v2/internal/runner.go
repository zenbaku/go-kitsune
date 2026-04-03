package internal

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// RunStages launches all stage functions concurrently, waits for all to finish,
// and returns the first non-nil error. Each stage is passed the errgroup context
// so that a failure in one stage cancels the context for all others.
func RunStages(ctx context.Context, stages []func(context.Context) error) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, stage := range stages {
		stage := stage
		g.Go(func() error { return stage(gctx) })
	}
	return g.Wait()
}

// DrainChan drains ch until it is closed. Used as a deferred cleanup so that
// upstream senders are never left blocked when a downstream stage exits.
func DrainChan[T any](ch <-chan T) {
	for range ch {
	}
}
