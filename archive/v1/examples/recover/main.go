// Example: recover — graceful error recovery and multi-hook observability.
//
// Demonstrates: MapRecover, Skip (offset), MultiHook, LogHook, Map, Filter, Collect.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
)

// metricsHook counts pipeline events for a simple operational summary.
type metricsHook struct {
	starts   atomic.Int64
	items    atomic.Int64
	errors   atomic.Int64
	drops    atomic.Int64
	restarts atomic.Int64
}

func (m *metricsHook) OnStageStart(_ context.Context, _ string) { m.starts.Add(1) }
func (m *metricsHook) OnItem(_ context.Context, _ string, _ time.Duration, err error) {
	m.items.Add(1)
	if err != nil {
		m.errors.Add(1)
	}
}
func (m *metricsHook) OnStageDone(_ context.Context, _ string, _, _ int64) {}
func (m *metricsHook) OnDrop(_ context.Context, _ string, _ any)           { m.drops.Add(1) }
func (m *metricsHook) OnStageRestart(_ context.Context, _ string, _ int, _ error) {
	m.restarts.Add(1)
}

func main() {
	// --- Skip(n): discard the header row of CSV-like data ---
	//
	// Real-world data often arrives with a header or preamble that should
	// not be processed. Skip(1) makes this a one-liner.
	fmt.Println("=== Skip: discard CSV header row ===")
	csvRows := []string{
		"id,name,score", // row 0 — header
		"1,alice,82",    // row 1
		"2,bob,45",      // row 2
		"3,carol,91",    // row 3
	}
	type Student struct {
		ID    int
		Name  string
		Score int
	}
	students, err := kitsune.Map(
		kitsune.FromSlice(csvRows).Skip(1), // drop the header
		func(_ context.Context, row string) (Student, error) {
			parts := strings.Split(row, ",")
			id, _ := strconv.Atoi(parts[0])
			score, _ := strconv.Atoi(parts[2])
			return Student{ID: id, Name: parts[1], Score: score}, nil
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Parsed %d students (header skipped)\n", len(students))
	for _, s := range students {
		fmt.Printf("  ID=%d  %-8s score=%d\n", s.ID, s.Name, s.Score)
	}

	// --- MapRecover: call an unreliable API with a fallback value ---
	//
	// Some items will fail. Instead of halting the pipeline or silently
	// dropping failures, MapRecover substitutes a default and lets processing
	// continue. The recover function can also log, emit a sentinel, etc.
	fmt.Println("\n=== MapRecover: unreliable enrichment with fallback ===")
	errServiceUnavailable := errors.New("service unavailable")
	type Item struct{ ID int }
	type EnrichedItem struct {
		ID     int
		Label  string
		Source string // "live" or "fallback"
	}

	items := kitsune.FromSlice([]Item{{1}, {2}, {3}, {4}, {5}})
	enriched, err := kitsune.MapRecover(
		items,
		func(_ context.Context, item Item) (EnrichedItem, error) {
			// Simulate: even IDs fail.
			if item.ID%2 == 0 {
				return EnrichedItem{}, errServiceUnavailable
			}
			return EnrichedItem{ID: item.ID, Label: fmt.Sprintf("live-%d", item.ID), Source: "live"}, nil
		},
		func(_ context.Context, item Item, err error) EnrichedItem {
			// Recovery: return a fallback value and tag it.
			fmt.Printf("  [recover] item %d failed (%v) — using fallback\n", item.ID, err)
			return EnrichedItem{ID: item.ID, Label: fmt.Sprintf("fallback-%d", item.ID), Source: "fallback"}
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println("Results:")
	for _, e := range enriched {
		fmt.Printf("  ID=%d  %-12s [%s]\n", e.ID, e.Label, e.Source)
	}

	// --- MapRecover + Skip in a combined pipeline ---
	//
	// Parse CSV (skip header), attempt conversion, recover from bad rows.
	fmt.Println("\n=== Skip + MapRecover: parse CSV, recover from bad rows ===")
	rawCSV := []string{
		"value", // header
		"10",
		"twenty", // malformed — not a number
		"30",
		"", // empty row
		"50",
	}
	type ParsedRow struct {
		Value int
		Valid bool
	}
	parsed, err := kitsune.MapRecover(
		kitsune.Map(
			kitsune.FromSlice(rawCSV).Skip(1), // skip header
			func(_ context.Context, s string) (string, error) { return s, nil },
		),
		func(_ context.Context, s string) (ParsedRow, error) {
			v, err := strconv.Atoi(s)
			if err != nil {
				return ParsedRow{}, fmt.Errorf("cannot parse %q: %w", s, err)
			}
			return ParsedRow{Value: v, Valid: true}, nil
		},
		func(_ context.Context, s string, err error) ParsedRow {
			return ParsedRow{Value: 0, Valid: false}
		},
	).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	valid := 0
	for _, row := range parsed {
		if row.Valid {
			valid++
		}
	}
	fmt.Printf("Rows parsed: %d total, %d valid, %d recovered\n",
		len(parsed), valid, len(parsed)-valid)

	// --- MultiHook: combine LogHook with a custom metrics hook ---
	//
	// MultiHook lets you attach multiple independent observers to a single
	// pipeline run. Each hook receives all events it implements — no
	// coordination required.
	fmt.Println("\n=== MultiHook: LogHook + metrics hook on the same run ===")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	metrics := &metricsHook{}
	combined := kitsune.MultiHook(kitsune.LogHook(logger), metrics)

	data := kitsune.FromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	processed := kitsune.Map(
		data.Filter(func(n int) bool { return n%2 == 0 }), // keep evens
		func(_ context.Context, n int) (int, error) { return n * n, nil },
		kitsune.WithName("square"),
	)
	results2, err := processed.Collect(context.Background(), kitsune.WithHook(combined))
	if err != nil {
		panic(err)
	}

	fmt.Println("\nSquared evens:", results2)
	fmt.Printf("\nMetrics summary:\n")
	fmt.Printf("  Stages started: %d\n", metrics.starts.Load())
	fmt.Printf("  Items processed: %d\n", metrics.items.Load())
	fmt.Printf("  Errors: %d\n", metrics.errors.Load())
}
