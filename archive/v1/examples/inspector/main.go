// Example: inspector — live web dashboard for a running pipeline.
//
// Demonstrates:
//   - An infinite Generate source so the pipeline never stops on its own
//   - A branching topology: Partition → two paths → Merge
//   - Broadcast fan-out within one branch
//   - Supervision restarts and DropOldest overflow visible in the dashboard
//   - Stop, Restart, and Pause/Resume controls from the UI
//
// Run with:
//
//	task inspector
//
// then open the URL printed to stdout.
// Press Ctrl-C to stop.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/inspector"
)

type event struct {
	id       int64
	priority string // "high" or "normal"
	value    int
}

func main() {
	insp := inspector.New()
	defer insp.Close()

	fmt.Printf("Inspector: %s\n\n", insp.URL())
	fmt.Println("Pipeline running — Ctrl-C or Stop button to exit.")
	fmt.Println()

	// ── Build pipeline once — Run can be called multiple times ─────────────
	var seq atomic.Int64
	source := kitsune.Generate(func(ctx context.Context, yield func(event) bool) error {
		ticker := time.NewTicker(200 * time.Microsecond) // ~5 000 events/sec
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				id := seq.Add(1)
				priority := "normal"
				if id%7 == 0 {
					priority = "high"
				}
				if !yield(event{id: id, priority: priority, value: rand.IntN(1000)}) {
					return nil
				}
			}
		}
	})

	// ── Stage: parse — light normalisation ────────────────────────────────
	parsed := kitsune.Map(
		source,
		func(_ context.Context, e event) (event, error) {
			e.value = e.value * 2
			return e, nil
		},
		kitsune.WithName("parse"),
		kitsune.Concurrency(2),
	)

	// ── Fork: split high-priority from normal ─────────────────────────────
	highPri, normalPri := kitsune.Partition(parsed, func(e event) bool {
		return e.priority == "high"
	})

	// ── High-priority path ────────────────────────────────────────────────
	// Broadcast to two independent processors, then merge their outputs.
	hiBranches := kitsune.Broadcast(highPri, 2)

	// Branch A: fast enrichment
	hiEnriched := kitsune.Map(
		hiBranches[0],
		func(_ context.Context, e event) (event, error) {
			time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
			e.value += 100
			return e, nil
		},
		kitsune.WithName("hi-enrich"),
		kitsune.Concurrency(3),
	)

	// Branch B: validation with occasional transient errors + supervision.
	// Error every ~500 items; window resets every 30 s so restarts never exhaust.
	var hiAttempts atomic.Int64
	hiValidated := kitsune.Map(
		hiBranches[1],
		func(_ context.Context, e event) (event, error) {
			n := hiAttempts.Add(1)
			if n%500 == 0 {
				return event{}, errors.New("transient validation error")
			}
			return e, nil
		},
		kitsune.WithName("hi-validate"),
		kitsune.Supervise(kitsune.RestartOnError(math.MaxInt, kitsune.FixedBackoff(10*time.Millisecond))),
	)

	hiMerged := kitsune.Merge(hiEnriched, hiValidated)

	// ── Normal-priority path ──────────────────────────────────────────────
	// Batch into groups of 20, process in bulk, then unbatch.
	batched := kitsune.Batch(normalPri, 20, kitsune.WithName("batch"))

	processed := kitsune.Map(
		batched,
		func(_ context.Context, items []event) ([]event, error) {
			time.Sleep(time.Duration(rand.IntN(500)) * time.Microsecond)
			for i := range items {
				items[i].value += 1
			}
			return items, nil
		},
		kitsune.WithName("bulk-process"),
		kitsune.Concurrency(2),
		kitsune.Buffer(8),
		kitsune.Overflow(kitsune.DropOldest),
	)

	normalOut := kitsune.Unbatch(processed)

	// ── Rejoin: merge both paths into a single sink ────────────────────────
	merged := kitsune.Merge(hiMerged, normalOut)

	// ── Sink: count events ─────────────────────────────────────────────────
	var total atomic.Int64
	sink := merged.ForEach(func(_ context.Context, _ event) error {
		total.Add(1)
		return nil
	}, kitsune.WithName("sink"))

	// ── Run loop: restart resumes here, stop exits ─────────────────────────
	gate := kitsune.NewGate()

	for {
		ctx, cancel := context.WithCancel(context.Background())

		// Capture fresh channel references for this run.
		// Both Stop and Restart cancel the current run context.
		cancelCh := insp.CancelCh()
		restartCh := insp.RestartCh()
		go func() {
			select {
			case <-cancelCh:
				cancel()
			case <-restartCh:
				cancel()
			case <-ctx.Done():
			}
		}()

		// Wire UI Pause/Resume buttons to the gate.
		go func() {
			for {
				select {
				case <-insp.PauseCh():
					gate.Pause()
				case <-insp.ResumeCh():
					gate.Resume()
				case <-ctx.Done():
					return
				}
			}
		}()

		if err := sink.Run(ctx, kitsune.WithHook(insp), kitsune.WithPauseGate(gate)); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("pipeline error: %v\n", err)
		}
		gate.Resume() // ensure gate is open for the next run
		cancel()

		// Restart if Restart was clicked; exit if Stop was clicked or Ctrl-C.
		select {
		case <-restartCh:
			fmt.Printf("Restarting… (%d events so far)\n", total.Load())
			continue
		case <-cancelCh:
			// Stop was clicked.
		default:
			// Pipeline ended for another reason (e.g. fatal error); keep
			// the inspector alive and wait for the user to click Restart.
			fmt.Printf("Pipeline ended (%d events). Click Restart or press Enter.\n", total.Load())
			select {
			case <-restartCh:
				fmt.Printf("Restarting…\n")
				continue
			case <-cancelCh:
			}
		}
		break
	}

	fmt.Printf("\nStopped. Processed %d events total.\n", total.Load())
	fmt.Printf("Inspector still at %s — press Enter to exit.\n", insp.URL())
	fmt.Scanln()
}
