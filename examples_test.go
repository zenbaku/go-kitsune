package kitsune_test

import (
	"os/exec"
	"testing"
)

// TestExamples runs every self-contained v2 example as a subprocess and fails
// if any example exits with a non-zero status or panics. This catches
// regressions that compile but break at runtime.
//
// Skipped under -short (i.e. `task test`). Run explicitly with:
//
//	go test -run TestExamples -timeout 120s .
//	task v2:test:examples
//
// Excluded from this test (interactive / requires external services):
//   - examples/inspector — infinite run loop, meant for manual exploration
func TestExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping example smoke tests in short mode")
	}
	examples := []string{
		"basic",
		"bloomdedup",
		"broadcast",
		"bufferwith",
		"caching",
		"channel",
		"circuitbreaker",
		"contextmapper",
		"concurrent",
		"concurrency-guide/enrich",
		"concurrency-guide/routing",
		"concurrency-guide/useragg",
		"devstore",
		"effect",
		"enrich",
		"expandmap",
		"fanout",
		"frequencies",
		"hooks",
		"isoptimized",
		"lookupby",
		"materialize",
		"perkeyratelimit",
		"ratelimit",
		"randomsample",
		"reducewhile",
		"runningtotal",
		"segment",
		"single",
		"stages",
		"switchmap",
		"ticker",
		"timeout",
		"tomap",
		"ttldedup",
		"typedreturn",
		"within",
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
