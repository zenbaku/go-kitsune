// Package kfile provides file, CSV, and JSONL sources and sinks for kitsune
// pipelines. All functions accept [io.Reader] or [io.Writer]; the caller owns
// file handles and is responsible for opening and closing them. Kitsune will
// never open or close files.
//
// Read lines from a file:
//
//	f, _ := os.Open("data.txt")
//	defer f.Close()
//	kfile.Lines(f).ForEach(process).Run(ctx)
//
// Write JSON lines to a file:
//
//	out, _ := os.Create("results.jsonl")
//	defer out.Close()
//	pipe.ForEach(kfile.WriteJSON[Result](out)).Run(ctx)
//
// Delivery semantics: not applicable. kfile wraps local I/O with no broker
// or ack mechanism. Errors from the underlying reader or writer terminate
// the pipeline immediately.
package kfile

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	kitsune "github.com/zenbaku/go-kitsune"
)

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

// Lines creates a Pipeline that emits one string per line from r.
func Lines(r io.Reader) *kitsune.Pipeline[string] {
	return kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !yield(scanner.Text()) {
				return nil
			}
		}
		return scanner.Err()
	})
}

// CSVOption configures CSV source behavior.
type CSVOption func(*csvConfig)

type csvConfig struct {
	skipHeader bool
	comma      rune
}

// SkipHeader skips the first row when reading CSV.
func SkipHeader() CSVOption { return func(c *csvConfig) { c.skipHeader = true } }

// Comma sets the field delimiter (default: ',').
func Comma(r rune) CSVOption { return func(c *csvConfig) { c.comma = r } }

// CSV creates a Pipeline that emits one []string row per CSV record.
func CSV(r io.Reader, opts ...CSVOption) *kitsune.Pipeline[[]string] {
	cfg := csvConfig{comma: ','}
	for _, opt := range opts {
		opt(&cfg)
	}
	return kitsune.Generate(func(ctx context.Context, yield func([]string) bool) error {
		reader := csv.NewReader(r)
		reader.Comma = cfg.comma
		first := true
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			record, err := reader.Read()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if first && cfg.skipHeader {
				first = false
				continue
			}
			first = false
			if !yield(record) {
				return nil
			}
		}
	})
}

// JSON creates a Pipeline that decodes one JSON object per line (JSONL/NDJSON).
func JSON[T any](r io.Reader) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		dec := json.NewDecoder(r)
		for dec.More() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var item T
			if err := dec.Decode(&item); err != nil {
				return err
			}
			if !yield(item) {
				return nil
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Sinks
// ---------------------------------------------------------------------------

// WriteLines returns a sink that writes each string as a line to w.
func WriteLines(w io.Writer) func(context.Context, string) error {
	mu := &sync.Mutex{}
	return func(_ context.Context, s string) error {
		mu.Lock()
		defer mu.Unlock()
		_, err := fmt.Fprintln(w, s)
		return err
	}
}

// WriteCSV returns a sink that writes each []string row as a CSV record.
// If header is non-nil, it is written before the first data row.
func WriteCSV(w io.Writer, header []string) func(context.Context, []string) error {
	mu := &sync.Mutex{}
	writer := csv.NewWriter(w)
	headerWritten := header == nil // skip if no header
	return func(_ context.Context, row []string) error {
		mu.Lock()
		defer mu.Unlock()
		if !headerWritten {
			if err := writer.Write(header); err != nil {
				return err
			}
			headerWritten = true
		}
		if err := writer.Write(row); err != nil {
			return err
		}
		writer.Flush()
		return writer.Error()
	}
}

// WriteJSON returns a sink that writes each item as a JSON line (JSONL).
func WriteJSON[T any](w io.Writer) func(context.Context, T) error {
	mu := &sync.Mutex{}
	enc := json.NewEncoder(w)
	return func(_ context.Context, item T) error {
		mu.Lock()
		defer mu.Unlock()
		return enc.Encode(item)
	}
}
