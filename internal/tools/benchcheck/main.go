// benchcheck compares two benchstat output files and exits with a non-zero
// status if any benchmark regresses beyond a configurable threshold.
//
// Usage:
//
//	go run ./internal/tools/benchcheck [flags] baseline.txt current.txt
//
// Flags:
//
//	-threshold float   percent regression that triggers failure (default 15)
//	-metric string     which benchstat unit to gate on (default "sec/op")
//
// The tool runs:
//
//	benchstat -format=csv baseline.txt current.txt
//
// and parses the output. Each section begins with a unit header line
// (e.g. ",sec/op,CI,..."). Data rows have the form:
//
//	name, A_val, A_CI, B_val, B_CI, delta, P
//
// A row is a regression when:
//  1. Both A and B columns are non-empty (benchmark exists in both files).
//  2. The delta is NOT "~" (result is statistically significant).
//  3. The parsed delta percentage exceeds +threshold.
//
// Added/removed benchmarks produce a warning but do not fail the check.
// Only the configured metric contributes to the exit code.
package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func main() {
	threshold := flag.Float64("threshold", 15.0, "percent regression that triggers failure")
	metric := flag.String("metric", "sec/op", "benchstat unit to gate on")
	flag.Parse()

	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: benchcheck [flags] baseline.txt current.txt\n")
		os.Exit(2)
	}

	baseline := flag.Arg(0)
	current := flag.Arg(1)

	for _, f := range []string{baseline, current} {
		if _, err := os.Stat(f); err != nil {
			fmt.Fprintf(os.Stderr, "benchcheck: cannot open %s: %v\n", f, err)
			os.Exit(2)
		}
	}

	out, err := runBenchstat(baseline, current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchcheck: benchstat failed: %v\n", err)
		os.Exit(2)
	}

	regressions, warnings := parseSections(out, *metric, *threshold)

	for _, w := range warnings {
		fmt.Println("WARN:", w)
	}

	if len(regressions) == 0 {
		fmt.Printf("benchcheck: no regressions above %.1f%% for %s\n", *threshold, *metric)
		return
	}

	fmt.Printf("\nbenchcheck: FAILED — %d regression(s) above %.1f%% for %s:\n\n", len(regressions), *threshold, *metric)
	for _, r := range regressions {
		fmt.Printf("  REGRESSION  %-60s  %s\n", r.name, r.delta)
	}
	fmt.Println()
	os.Exit(1)
}

// runBenchstat invokes benchstat with CSV output and returns stdout.
func runBenchstat(baseline, current string) (string, error) {
	cmd := exec.Command("benchstat", "-format=csv", baseline, current) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%w\nstderr: %s", exitErr, exitErr.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

type regression struct {
	name  string
	delta string
}

// parseSections reads benchstat -format=csv output which contains multiple
// sections separated by blank lines. Each section has a file-name header row,
// a column-name header row, and then data rows.
//
// Lines before the first CSV section (goos:/goarch:/pkg:/cpu:) are skipped.
func parseSections(output, gatedMetric string, threshold float64) (regressions []regression, warnings []string) {
	// Split into logical sections at blank lines.
	// Each section is a slice of non-empty lines.
	var sections [][]string
	var current []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(current) > 0 {
				sections = append(sections, current)
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, current)
	}

	for _, sec := range sections {
		regs, warns := parseSection(sec, gatedMetric, threshold)
		regressions = append(regressions, regs...)
		warnings = append(warnings, warns...)
	}
	return
}

// parseSection handles one benchstat CSV section.
// A section looks like:
//
//	goos: darwin          (optional, only in first section)
//	goarch: arm64
//	pkg: ...
//	cpu: ...
//	,file1.txt,,file2.txt,,,
//	,sec/op,CI,sec/op,CI,vs base,P
//	BenchmarkFoo-8,...
//	geomean,...
func parseSection(lines []string, gatedMetric string, threshold float64) (regressions []regression, warnings []string) {
	// Find the unit header row: starts with "," and contains the metric name.
	unitHeaderIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(line, ",") {
			// First comma-prefixed line is the file header; second is the unit header.
			// The unit header contains e.g. ",sec/op,CI,sec/op,CI,vs base,P".
			if unitHeaderIdx == -1 {
				// Skip the file-names header.
				unitHeaderIdx = i
				continue
			}
			// This is the unit header.
			unitHeaderIdx = i
			break
		}
	}
	if unitHeaderIdx < 0 {
		// No CSV content in this section.
		return
	}

	// Parse the unit from the header row.
	unitHeader, err := parseLine(lines[unitHeaderIdx])
	if err != nil || len(unitHeader) < 2 {
		return
	}
	unit := strings.TrimSpace(unitHeader[1])
	if unit != gatedMetric {
		return
	}

	// Determine column indices from unit header.
	// Format: ["", "sec/op", "CI", "sec/op", "CI", "vs base", "P"]
	// col 0: name, col 1: A_val, col 2: A_CI, col 3: B_val, col 4: B_CI,
	// col 5: delta ("vs base"), col 6: P
	const (
		colName  = 0
		colA     = 1
		colB     = 3
		colDelta = 5
		colNote  = 6
	)

	// Process data rows.
	for _, line := range lines[unitHeaderIdx+1:] {
		if !strings.Contains(line, ",") {
			continue
		}
		rec, err := parseLine(line)
		if err != nil || len(rec) <= colNote {
			continue
		}

		name := strings.TrimSpace(rec[colName])
		aVal := strings.TrimSpace(rec[colA])
		bVal := strings.TrimSpace(rec[colB])
		delta := strings.TrimSpace(rec[colDelta])
		note := strings.TrimSpace(rec[colNote])

		// Skip summary row.
		if name == "geomean" {
			continue
		}

		// Benchmark added or removed.
		if aVal == "" || bVal == "" {
			if bVal == "" {
				warnings = append(warnings, fmt.Sprintf("benchmark removed: %s", name))
			} else {
				warnings = append(warnings, fmt.Sprintf("benchmark added: %s", name))
			}
			continue
		}

		// "~" in delta means no statistically significant difference.
		if delta == "~" {
			continue
		}

		// Note column contains "p=0.05 n=10" — ignore it for now.
		_ = note

		pct, err := parsePercent(delta)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("benchcheck: could not parse delta %q for %s: %v", delta, name, err))
			continue
		}

		if pct > threshold {
			regressions = append(regressions, regression{name: name, delta: delta})
		}
	}

	return
}

// parseLine parses a single CSV line.
func parseLine(line string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(line))
	return r.Read()
}

// parsePercent converts "+12.34%" or "-5.00%" to a float64.
// Returns positive for regressions (slowdowns) and negative for improvements.
func parsePercent(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
