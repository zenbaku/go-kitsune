// Example: sessionwindow — gap-based activity session grouping.
//
// Demonstrates: SessionWindow (groups items into sessions separated by periods
// of inactivity; flushes when no item arrives within the gap duration).
package main

import (
	"context"
	"fmt"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

func main() {
	// Simulate user activity events: two bursts separated by a pause.
	source := kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		// First activity burst.
		for _, event := range []string{"page_view", "click", "scroll"} {
			if !yield(event) {
				return nil
			}
		}

		// Pause longer than the session gap — triggers a session flush.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}

		// Second activity burst.
		for _, event := range []string{"click", "form_submit"} {
			if !yield(event) {
				return nil
			}
		}

		return nil
	})

	// Group events into sessions: flush when 100ms of inactivity passes.
	sessions := kitsune.SessionWindow(source, 100*time.Millisecond)

	results, err := sessions.Collect(context.Background())
	if err != nil {
		panic(err)
	}

	for i, session := range results {
		fmt.Printf("Session %d (%d events): %v\n", i+1, len(session), session)
	}
	// Expected (timing-dependent):
	// Session 1 (3 events): [page_view click scroll]
	// Session 2 (2 events): [click form_submit]
}
