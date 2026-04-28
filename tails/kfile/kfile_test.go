package kfile_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kfile"
)

func TestLines(t *testing.T) {
	input := strings.NewReader("alpha\nbravo\ncharlie\n")
	results, err := kfile.Lines(input).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"alpha", "bravo", "charlie"}
	if len(results) != len(expected) {
		t.Fatalf("expected %d lines, got %d: %v", len(expected), len(results), results)
	}
	for i, v := range results {
		if v != expected[i] {
			t.Errorf("line %d = %q, want %q", i, v, expected[i])
		}
	}
}

func TestCSV(t *testing.T) {
	input := strings.NewReader("name,age\nalice,30\nbob,25\n")
	results, err := kfile.CSV(input, kfile.SkipHeader()).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(results))
	}
	if results[0][0] != "alice" || results[0][1] != "30" {
		t.Errorf("row 0: %v", results[0])
	}
}

func TestCSVCustomComma(t *testing.T) {
	input := strings.NewReader("a;b;c\n1;2;3\n")
	results, err := kfile.CSV(input, kfile.Comma(';')).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(results))
	}
}

func TestJSON(t *testing.T) {
	type Item struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	input := strings.NewReader(`{"name":"alice","age":30}
{"name":"bob","age":25}
`)
	results, err := kfile.JSON[Item](input).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results))
	}
	if results[0].Name != "alice" || results[1].Age != 25 {
		t.Fatalf("unexpected: %v", results)
	}
}

func TestWriteLines(t *testing.T) {
	var buf bytes.Buffer
	input := kitsune.FromSlice([]string{"hello", "world"})
	_, err := input.ForEach(kfile.WriteLines(&buf)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello\nworld\n" {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestWriteCSV(t *testing.T) {
	var buf bytes.Buffer
	input := kitsune.FromSlice([][]string{{"alice", "30"}, {"bob", "25"}})
	_, err := input.ForEach(kfile.WriteCSV(&buf, []string{"name", "age"})).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := "name,age\nalice,30\nbob,25\n"
	if buf.String() != expected {
		t.Fatalf("expected %q, got %q", expected, buf.String())
	}
}

func TestWriteJSON(t *testing.T) {
	type Item struct {
		Name string `json:"name"`
	}
	var buf bytes.Buffer
	input := kitsune.FromSlice([]Item{{Name: "alice"}, {Name: "bob"}})
	_, err := input.ForEach(kfile.WriteJSON[Item](&buf)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := "{\"name\":\"alice\"}\n{\"name\":\"bob\"}\n"
	if buf.String() != expected {
		t.Fatalf("expected %q, got %q", expected, buf.String())
	}
}

func TestLinesE2E(t *testing.T) {
	// Pipeline: read lines → uppercase → write lines.
	input := strings.NewReader("hello\nworld\n")
	var output bytes.Buffer

	lines := kfile.Lines(input)
	upper := kitsune.Map(lines, func(_ context.Context, s string) (string, error) {
		return strings.ToUpper(s), nil
	})
	_, err := upper.ForEach(kfile.WriteLines(&output)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "HELLO\nWORLD\n" {
		t.Fatalf("unexpected: %q", output.String())
	}
}

func TestCSVToJSON(t *testing.T) {
	// Pipeline: read CSV → map to struct → write JSONL.
	type Record struct {
		Name string `json:"name"`
		Age  string `json:"age"`
	}

	csvInput := strings.NewReader("name,age\nalice,30\nbob,25\n")
	var jsonOutput bytes.Buffer

	rows := kfile.CSV(csvInput, kfile.SkipHeader())
	records := kitsune.Map(rows, func(_ context.Context, row []string) (Record, error) {
		return Record{Name: row[0], Age: row[1]}, nil
	})
	_, err := records.ForEach(kfile.WriteJSON[Record](&jsonOutput)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expected := "{\"name\":\"alice\",\"age\":\"30\"}\n{\"name\":\"bob\",\"age\":\"25\"}\n"
	if jsonOutput.String() != expected {
		t.Fatalf("expected %q, got %q", expected, jsonOutput.String())
	}
}
