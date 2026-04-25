// Example: Effect models an externally-visible side effect with retry,
// per-attempt timeout, and required-vs-best-effort semantics. TryEffect
// splits the outcome stream into ok and failed branches.
//
//	go run ./examples/effect
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zenbaku/go-kitsune"
)

type Message struct {
	ID   int
	Body string
}

func main() {
	ctx := context.Background()

	src := kitsune.FromSlice([]Message{
		{1, "hello"}, {2, "world"}, {3, "fail-once"}, {4, "drop-this"},
	})

	attempts := make(map[int]int)
	publish := func(_ context.Context, m Message) (string, error) {
		attempts[m.ID]++
		switch m.ID {
		case 3:
			// Fail once, then succeed on retry.
			if attempts[m.ID] < 2 {
				return "", errors.New("transient")
			}
			return fmt.Sprintf("ack:%d", m.ID), nil
		case 4:
			return "", errors.New("permanent")
		default:
			return fmt.Sprintf("ack:%d", m.ID), nil
		}
	}

	okP, failP := kitsune.TryEffect(src, publish,
		kitsune.EffectPolicy{
			Required:       true,
			Retry:          kitsune.RetryUpTo(3, kitsune.FixedBackoff(10*time.Millisecond)),
			AttemptTimeout: 100 * time.Millisecond,
		},
	)

	okRunner := okP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
		fmt.Printf("OK    id=%d ack=%s\n", o.Input.ID, o.Result)
		return nil
	})
	failRunner := failP.ForEach(func(_ context.Context, o kitsune.EffectOutcome[Message, string]) error {
		fmt.Printf("FAIL  id=%d err=%v\n", o.Input.ID, o.Err)
		return nil
	})

	runner, err := kitsune.MergeRunners(okRunner, failRunner)
	if err != nil {
		panic(err)
	}
	if _, err := runner.Run(ctx); err != nil {
		panic(err)
	}
}
