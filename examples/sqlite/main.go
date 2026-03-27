// Example: sqlite — reading from and writing to SQLite.
//
// Demonstrates: ksqlite.Query (source), ksqlite.Insert (sink),
// ksqlite.BatchInsert (batched sink), pipeline with filter + transform.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/ksqlite"
	_ "modernc.org/sqlite"
)

type Employee struct {
	ID         int
	Name       string
	Department string
	Salary     int
}

func scanEmployee(rows *sql.Rows) (Employee, error) {
	var e Employee
	err := rows.Scan(&e.ID, &e.Name, &e.Department, &e.Salary)
	return e, err
}

func main() {
	ctx := context.Background()

	// User-managed connection.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // required for :memory: SQLite

	// Setup schema and seed data.
	db.Exec(`CREATE TABLE employees (
		id INTEGER PRIMARY KEY, name TEXT, department TEXT, salary INTEGER
	)`)
	db.Exec(`CREATE TABLE senior_staff (
		id INTEGER PRIMARY KEY, name TEXT, department TEXT, salary INTEGER
	)`)
	for _, e := range []Employee{
		{1, "Alice", "Engineering", 120000},
		{2, "Bob", "Marketing", 85000},
		{3, "Carol", "Engineering", 145000},
		{4, "Dave", "Marketing", 72000},
		{5, "Eve", "Engineering", 130000},
		{6, "Frank", "Sales", 90000},
	} {
		db.Exec("INSERT INTO employees VALUES (?, ?, ?, ?)", e.ID, e.Name, e.Department, e.Salary)
	}

	// --- Query source → filter → transform → insert sink ---
	fmt.Println("=== Senior engineering staff (salary >= 125k) ===")
	source := ksqlite.Query(db, "SELECT id, name, department, salary FROM employees ORDER BY id", scanEmployee)

	senior := source.
		Filter(func(e Employee) bool { return e.Department == "Engineering" }).
		Filter(func(e Employee) bool { return e.Salary >= 125000 }).
		Tap(func(e Employee) { fmt.Printf("  found: %s ($%d)\n", e.Name, e.Salary) })

	err = senior.ForEach(ksqlite.Insert(db, "senior_staff",
		[]string{"id", "name", "department", "salary"},
		func(e Employee) []any { return []any{e.ID, e.Name, e.Department, e.Salary} },
	)).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Verify.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM senior_staff").Scan(&count)
	fmt.Printf("  → %d senior engineers inserted\n", count)

	// --- Query with parameters ---
	fmt.Println("\n=== Parameterized query: Marketing department ===")
	marketing := ksqlite.Query(db,
		"SELECT id, name, department, salary FROM employees WHERE department = ? ORDER BY name",
		scanEmployee, "Marketing",
	)
	marketers, _ := marketing.Collect(ctx)
	for _, e := range marketers {
		fmt.Printf("  %s: $%d\n", e.Name, e.Salary)
	}

	// --- Batch insert ---
	fmt.Println("\n=== Batch insert 100 records ===")
	db.Exec(`CREATE TABLE batch_test (id INTEGER PRIMARY KEY, value TEXT)`)

	type Record struct {
		ID    int
		Value string
	}
	records := make([]Record, 100)
	for i := range records {
		records[i] = Record{ID: i + 1, Value: fmt.Sprintf("item-%d", i+1)}
	}

	batched := kitsune.Batch(kitsune.FromSlice(records), 25)
	err = batched.ForEach(ksqlite.BatchInsert(db, "batch_test",
		[]string{"id", "value"},
		func(r Record) []any { return []any{r.ID, r.Value} },
	)).Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	db.QueryRow("SELECT COUNT(*) FROM batch_test").Scan(&count)
	fmt.Printf("  → %d records inserted in batches of 25\n", count)
}
