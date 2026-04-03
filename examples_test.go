package kitsune_test

import (
	"os/exec"
	"testing"
)

// TestExamples runs every self-contained example as a subprocess and fails if
// any example exits with a non-zero status or panics. This catches regressions
// that compile successfully but break at runtime.
//
// Skipped under -short (i.e. `task test`). Run explicitly with:
//
//	go test -run TestExamples -timeout 120s .
//	task test:examples
//
// Excluded from this test (handled elsewhere or require external services):
//   - examples/inspector  — interactive, infinite run loop
//   - examples/redis      — own go.mod, needs a live Redis instance
//   - examples/sqlite     — own go.mod, needs a live SQLite file
//   - examples/files      — own go.mod, depends on tails/kfile module
//   - examples/http       — own go.mod, depends on tails/khttp module
func TestExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping example smoke tests in short mode")
	}
	examples := []string{
		"aggregate",
		"basic",
		"batch",
		"broadcast",
		"channel",
		"collectors",
		"compose",
		"concatmap",
		"concurrent",
		"dedupe",
		"enrich",
		"errors",
		"fanout",
		"filter",
		"flatmap",
		"generate",
		"groupby",
		"iter",
		"mapresult",
		"metrics",
		"overflow",
		"pause",
		"pairwise",
		"recover",
		"reduce",
		"reshape",
		"scan",
		"slidingwindow",
		"stages",
		"state",
		"streams",
		"supervise",
		"ticker",
		"timebased",
		"timeout",
		"transform",
		"window",
		"withlatestfrom",
		"zip",
		"zipwith",
		// gap-fill features
		"circuitbreaker",
		"metricsapi",
		"pool",
		"ratelimit",
		// switch/exhaust map operators
		"switchmap",
		"exhaustmap",
		// multi-stream operators
		"combinelatest",
		"balance",
		// testclock / deterministic time
		"testclock",
		// per-entity keyed state
		"mapwithkey",
	}

	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("go", "run", "./examples/"+name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("example %q failed:\n%s", name, out)
			}
		})
	}
}
