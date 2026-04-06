// Example: files — reading and writing CSV, JSONL, and plain text.
//
// Demonstrates: kfile.CSV, kfile.JSON, kfile.Lines, kfile.WriteJSON, kfile.WriteCSV.
package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kfile"
)

type Person struct {
	Name string `json:"name"`
	City string `json:"city"`
}

func main() {
	// --- CSV → transform → JSONL ---
	fmt.Println("=== CSV to JSONL ===")
	csvData := strings.NewReader("name,city\nAlice,Tokyo\nBob,Berlin\nCarol,Lima\n")

	rows := kfile.CSV(csvData, kfile.SkipHeader())
	people := kitsune.Map(rows, func(_ context.Context, row []string) (Person, error) {
		return Person{Name: row[0], City: row[1]}, nil
	})

	var jsonBuf bytes.Buffer
	err := people.ForEach(kfile.WriteJSON[Person](&jsonBuf)).Run(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Print(jsonBuf.String())

	// --- JSONL → transform → CSV ---
	fmt.Println("\n=== JSONL to CSV ===")
	jsonData := strings.NewReader(`{"name":"Dave","city":"Paris"}
{"name":"Eve","city":"Seoul"}
`)

	parsed := kfile.JSON[Person](jsonData)
	backToRows := kitsune.Map(parsed, func(_ context.Context, p Person) ([]string, error) {
		return []string{p.Name, strings.ToUpper(p.City)}, nil
	})

	var csvBuf bytes.Buffer
	err = backToRows.ForEach(kfile.WriteCSV(&csvBuf, []string{"name", "city"})).Run(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Print(csvBuf.String())

	// --- Lines → filter → lines ---
	fmt.Println("\n=== Filter log lines ===")
	logInput := strings.NewReader("INFO: started\nERROR: disk full\nINFO: request\nERROR: timeout\n")

	lines := kfile.Lines(logInput)
	errors := lines.Filter(func(s string) bool { return strings.HasPrefix(s, "ERROR") })

	var outBuf bytes.Buffer
	err = errors.ForEach(kfile.WriteLines(&outBuf)).Run(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Print(outBuf.String())
}
