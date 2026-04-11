// tailmatrix runs every tail integration package and prints a unified
// pass/fail/skip matrix.
//
// Usage:
//
//	go run ./internal/tools/tailmatrix [flags]
//
// Flags:
//
//	-root string       repo root directory (default ".")
//	-tails string      comma-separated tail names; default = all discovered under tails/
//	-timeout duration  per-tail test timeout (default 60s)
//	-race              pass -race to go test (default true)
//	-require string    comma-separated tail names that must PASS (SKIP promoted to FAIL)
//	-json              emit JSON summary to stdout instead of the table
//
// The tool exits 0 if no failures, 1 if any tail failed, 2 on usage error.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// status represents the outcome of a tail test run.
type status int

const (
	statusPass status = iota
	statusFail
	statusSkip
)

func (s status) String() string {
	switch s {
	case statusPass:
		return "PASS"
	case statusFail:
		return "FAIL"
	case statusSkip:
		return "SKIP"
	}
	return "UNKNOWN"
}

// result holds the outcome for one tail package.
type result struct {
	Tail    string
	Status  status
	Reason  string
	Elapsed time.Duration
	Output  string // only populated on FAIL
}

// testEvent mirrors the shape emitted by go test -json.
type testEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

func main() {
	rootFlag := flag.String("root", ".", "repo root directory")
	tailsFlag := flag.String("tails", "", "comma-separated tail names; default = all discovered")
	timeoutFlag := flag.Duration("timeout", 60*time.Second, "per-tail test timeout")
	raceFlag := flag.Bool("race", true, "pass -race to go test")
	requireFlag := flag.String("require", "", "comma-separated tails that must PASS (SKIP is promoted to FAIL)")
	jsonFlag := flag.Bool("json", false, "emit JSON summary to stdout")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tailmatrix: invalid root: %v\n", err)
		os.Exit(2)
	}

	// Discover or parse the list of tails to run.
	var tails []string
	if *tailsFlag != "" {
		for _, t := range strings.Split(*tailsFlag, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tails = append(tails, t)
			}
		}
	} else {
		tails, err = discoverTails(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tailmatrix: discovery failed: %v\n", err)
			os.Exit(2)
		}
	}

	// Build the required-pass set.
	required := map[string]bool{}
	if *requireFlag != "" {
		for _, t := range strings.Split(*requireFlag, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				required[t] = true
			}
		}
	}

	// Run tails sequentially to avoid port contention.
	var results []result
	for _, tail := range tails {
		r := runTail(root, tail, *timeoutFlag, *raceFlag)
		// Promote required skips to failures.
		if r.Status == statusSkip && required[tail] {
			r.Status = statusFail
			if r.Reason == "" {
				r.Reason = "required tail skipped"
			} else {
				r.Reason = "required tail skipped: " + r.Reason
			}
		}
		results = append(results, r)
	}

	// Sort for stable output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Tail < results[j].Tail
	})

	if *jsonFlag {
		emitJSON(os.Stdout, results)
	} else {
		renderTable(os.Stdout, results)
	}

	// Exit 1 if any failures.
	for _, r := range results {
		if r.Status == statusFail {
			os.Exit(1)
		}
	}
}

// discoverTails returns the names of subdirectories under tails/ that contain
// a go.mod file.
func discoverTails(root string) ([]string, error) {
	tailsDir := filepath.Join(root, "tails")
	entries, err := os.ReadDir(tailsDir)
	if err != nil {
		return nil, fmt.Errorf("reading tails/: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		modPath := filepath.Join(tailsDir, e.Name(), "go.mod")
		if _, err := os.Stat(modPath); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// runTail executes go test -json for a single tail package and classifies the
// result.
func runTail(root, tail string, timeout time.Duration, race bool) result {
	dir := filepath.Join(root, "tails", tail)
	start := time.Now()

	args := []string{"test", "-json", "-count=1",
		fmt.Sprintf("-timeout=%s", timeout)}
	if race {
		args = append(args, "-race")
	}
	args = append(args, "./...")

	cmd := exec.Command("go", args...) //nolint:gosec
	cmd.Dir = dir

	// Capture stdout (JSON events) and stderr (build errors) separately.
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	elapsed := time.Since(start)

	events, parseErr := parseJSONStream(strings.NewReader(stdoutBuf.String()))
	if parseErr != nil && len(events) == 0 {
		// Likely a build/link failure — JSON output is garbage or empty.
		reason := "build error"
		if first := firstLine(stderrBuf.String()); first != "" {
			reason = "build error: " + first
		} else if first := firstLine(stdoutBuf.String()); first != "" {
			reason = "build error: " + first
		}
		return result{
			Tail:    tail,
			Status:  statusFail,
			Reason:  reason,
			Elapsed: elapsed,
			Output:  stdoutBuf.String() + stderrBuf.String(),
		}
	}

	st, reason := classify(events, runErr, stderrBuf.String())
	r := result{
		Tail:    tail,
		Status:  st,
		Reason:  reason,
		Elapsed: elapsed,
	}
	if st == statusFail {
		// Attach output for display below the table.
		combined := stdoutBuf.String()
		if s := stderrBuf.String(); s != "" {
			combined += s
		}
		r.Output = tailLines(combined, 100)
	}
	return r
}

// classify determines status and reason from JSON events and the process exit
// error.
//
// Note: when all tests in a package are skipped, go test still emits a
// package-level "pass" action. We classify based on test-level actions only
// (those where e.Test != "") to correctly distinguish SKIP from PASS.
func classify(events []testEvent, exitErr error, stderr string) (status, string) {
	var (
		testPassCount int
		testFailCount int
		skipMessages  []string
		failNames     []string
	)

	// Track the most recent non-boilerplate output line per test for skip-reason
	// extraction.
	lastOutputForTest := map[string]string{}

	for _, e := range events {
		switch e.Action {
		case "pass":
			if e.Test != "" {
				testPassCount++
			}
		case "fail":
			if e.Test != "" {
				testFailCount++
				failNames = append(failNames, e.Test)
			} else {
				// Package-level fail with no individual test failures (e.g. build
				// error surfaced here).
				testFailCount++
			}
		case "skip":
			if e.Test != "" {
				msg := extractSkipMessage(lastOutputForTest[e.Test])
				skipMessages = append(skipMessages, msg)
			}
		case "output":
			if e.Test != "" {
				trimmed := strings.TrimRight(e.Output, "\n")
				trimmed = strings.TrimSpace(trimmed)
				if trimmed != "" &&
					!strings.HasPrefix(trimmed, "=== RUN") &&
					!strings.HasPrefix(trimmed, "--- SKIP") &&
					!strings.HasPrefix(trimmed, "--- PASS") &&
					!strings.HasPrefix(trimmed, "--- FAIL") {
					lastOutputForTest[e.Test] = trimmed
				}
			}
		}
	}

	switch {
	case testFailCount > 0 || (exitErr != nil && !isExitCode(exitErr, 0)):
		reason := ""
		if len(failNames) > 0 {
			reason = failNames[0]
		} else if stderr != "" {
			reason = firstLine(stderr)
		}
		return statusFail, reason

	case testPassCount == 0 && len(skipMessages) > 0:
		// All tests were skipped.
		return statusSkip, firstUniqueSkipReason(skipMessages)

	case testPassCount == 0 && len(skipMessages) == 0 && exitErr == nil:
		// No test files or empty package.
		return statusPass, "no tests"

	default:
		return statusPass, ""
	}
}

// parseJSONStream decodes a stream of test JSON events.
func parseJSONStream(r io.Reader) ([]testEvent, error) {
	var events []testEvent
	dec := json.NewDecoder(r)
	for {
		var e testEvent
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			// Non-fatal: some lines may be plain text (e.g. build output).
			// Return what we have so the caller can decide.
			return events, err
		}
		events = append(events, e)
	}
	return events, nil
}

// extractSkipMessage extracts a human-readable skip reason from a raw output
// line. The line is typically the argument passed to t.Skip/t.Skipf.
func extractSkipMessage(line string) string {
	// Lines sometimes look like:
	//   "    redis_test.go:17: Redis not available: dial tcp ..."
	//   "    MONGO_URI not set; skipping MongoDB integration tests"
	//   "    skip.go:15: KAFKA_BROKER not set — skipping integration test"
	if idx := strings.Index(line, ": "); idx != -1 {
		line = line[idx+2:]
	}
	return strings.TrimSpace(line)
}

// firstUniqueSkipReason returns the first non-empty, unique skip reason.
func firstUniqueSkipReason(msgs []string) string {
	seen := map[string]bool{}
	for _, m := range msgs {
		if m != "" && !seen[m] {
			seen[m] = true
			return m
		}
	}
	return "skipped"
}

// firstLine returns the first non-empty line from s.
func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line
		}
	}
	return ""
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// isExitCode reports whether err is an *exec.ExitError with a specific code.
func isExitCode(err error, code int) bool {
	if err == nil {
		return code == 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode() == code
	}
	return false
}

// renderTable writes the results as an ASCII table.
func renderTable(w io.Writer, results []result) {
	fmt.Fprintln(w, "=== Tail integration test matrix ===")
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Tail\tStatus\tTime\tReason")
	fmt.Fprintln(tw, "----\t------\t----\t------")

	var pass, fail, skip int
	var total time.Duration
	for _, r := range results {
		switch r.Status {
		case statusPass:
			pass++
		case statusFail:
			fail++
		case statusSkip:
			skip++
		}
		total += r.Elapsed
		fmt.Fprintf(tw, "%s\t%s\t%.1fs\t%s\n",
			r.Tail, r.Status, r.Elapsed.Seconds(), r.Reason)
	}
	tw.Flush()

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Summary: %d passed, %d failed, %d skipped   (total %.1fs)\n",
		pass, fail, skip, total.Seconds())

	// Print captured output for failing tails below the table.
	for _, r := range results {
		if r.Status == statusFail && r.Output != "" {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "--- FAIL: %s ---\n", r.Tail)
			fmt.Fprintln(w, r.Output)
		}
	}
}

// jsonSummary is the JSON output shape.
type jsonSummary struct {
	Tails   []jsonTail `json:"tails"`
	Summary struct {
		Pass    int     `json:"pass"`
		Fail    int     `json:"fail"`
		Skip    int     `json:"skip"`
		Elapsed float64 `json:"elapsed_seconds"`
	} `json:"summary"`
}

type jsonTail struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Reason  string  `json:"reason,omitempty"`
	Elapsed float64 `json:"elapsed_seconds"`
}

// emitJSON writes a JSON summary of the results.
func emitJSON(w io.Writer, results []result) {
	var summary jsonSummary
	var total time.Duration
	for _, r := range results {
		jt := jsonTail{
			Name:    r.Tail,
			Status:  r.Status.String(),
			Reason:  r.Reason,
			Elapsed: r.Elapsed.Seconds(),
		}
		summary.Tails = append(summary.Tails, jt)
		total += r.Elapsed
		switch r.Status {
		case statusPass:
			summary.Summary.Pass++
		case statusFail:
			summary.Summary.Fail++
		case statusSkip:
			summary.Summary.Skip++
		}
	}
	summary.Summary.Elapsed = total.Seconds()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(summary)
}
