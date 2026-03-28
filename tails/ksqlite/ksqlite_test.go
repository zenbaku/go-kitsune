package ksqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/ksqlite"
	_ "modernc.org/sqlite"
)

type User struct {
	ID   int
	Name string
	Age  int
}

func scanUser(rows *sql.Rows) (User, error) {
	var u User
	err := rows.Scan(&u.ID, &u.Name, &u.Age)
	return u, err
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// SQLite :memory: creates a separate DB per connection.
	// Force a single connection so all operations see the same schema.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE users (
		id   INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		age  INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func seedUsers(t *testing.T, db *sql.DB, users []User) {
	t.Helper()
	for _, u := range users {
		_, err := db.Exec("INSERT INTO users (id, name, age) VALUES (?, ?, ?)", u.ID, u.Name, u.Age)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuerySource(t *testing.T) {
	db := openTestDB(t)
	seedUsers(t, db, []User{
		{1, "Alice", 30},
		{2, "Bob", 25},
		{3, "Carol", 35},
	})

	p := ksqlite.Query(db, "SELECT id, name, age FROM users ORDER BY id", scanUser)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 users, got %d", len(results))
	}
	if results[0].Name != "Alice" || results[1].Name != "Bob" || results[2].Name != "Carol" {
		t.Fatalf("unexpected users: %v", results)
	}
}

func TestQueryWithArgs(t *testing.T) {
	db := openTestDB(t)
	seedUsers(t, db, []User{
		{1, "Alice", 30},
		{2, "Bob", 25},
		{3, "Carol", 35},
	})

	p := ksqlite.Query(db, "SELECT id, name, age FROM users WHERE age > ? ORDER BY id", scanUser, 28)
	results, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 users with age > 28, got %d", len(results))
	}
	if results[0].Name != "Alice" || results[1].Name != "Carol" {
		t.Fatalf("unexpected users: %v", results)
	}
}

func TestInsertSink(t *testing.T) {
	db := openTestDB(t)

	users := []User{
		{1, "Alice", 30},
		{2, "Bob", 25},
		{3, "Carol", 35},
	}

	input := kitsune.FromSlice(users)
	err := input.ForEach(ksqlite.Insert(db, "users", []string{"id", "name", "age"}, func(u User) []any {
		return []any{u.ID, u.Name, u.Age}
	})).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify rows were inserted.
	readBack := ksqlite.Query(db, "SELECT id, name, age FROM users ORDER BY id", scanUser)
	results, err := readBack.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(results))
	}
	for i, u := range results {
		if u.ID != users[i].ID || u.Name != users[i].Name || u.Age != users[i].Age {
			t.Errorf("row %d: got %v, want %v", i, u, users[i])
		}
	}
}

func TestBatchInsertSink(t *testing.T) {
	db := openTestDB(t)

	users := make([]User, 50)
	for i := range users {
		users[i] = User{ID: i + 1, Name: fmt.Sprintf("user-%d", i+1), Age: 20 + i}
	}

	input := kitsune.FromSlice(users)
	batched := kitsune.Batch(input, 15)

	err := batched.ForEach(ksqlite.BatchInsert(db, "users", []string{"id", "name", "age"}, func(u User) []any {
		return []any{u.ID, u.Name, u.Age}
	})).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify all rows inserted.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 50 {
		t.Fatalf("expected 50 rows, got %d", count)
	}
}

func TestSQLiteE2E(t *testing.T) {
	// Full pipeline: read from source table → transform → filter → write to dest table.
	db := openTestDB(t)
	seedUsers(t, db, []User{
		{1, "Alice", 30},
		{2, "Bob", 17},
		{3, "Carol", 35},
		{4, "Dave", 15},
		{5, "Eve", 28},
	})

	// Create destination table.
	_, err := db.Exec(`CREATE TABLE adults (
		id   INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		age  INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	// Pipeline: read all users → filter adults → transform name → insert.
	source := ksqlite.Query(db, "SELECT id, name, age FROM users ORDER BY id", scanUser)
	adults := source.Filter(func(u User) bool { return u.Age >= 18 })
	transformed := kitsune.Map(adults, func(_ context.Context, u User) (User, error) {
		u.Name = fmt.Sprintf("%s (adult)", u.Name)
		return u, nil
	})

	err = transformed.ForEach(ksqlite.Insert(db, "adults", []string{"id", "name", "age"}, func(u User) []any {
		return []any{u.ID, u.Name, u.Age}
	})).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify destination.
	dest := ksqlite.Query(db, "SELECT id, name, age FROM adults ORDER BY id", scanUser)
	results, err := dest.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 adults, got %d: %v", len(results), results)
	}
	expected := []string{"Alice (adult)", "Carol (adult)", "Eve (adult)"}
	for i, u := range results {
		if u.Name != expected[i] {
			t.Errorf("adult %d: got %q, want %q", i, u.Name, expected[i])
		}
	}
}

func TestSQLiteWithDedupe(t *testing.T) {
	db := openTestDB(t)
	// Insert duplicate names.
	seedUsers(t, db, []User{
		{1, "Alice", 30},
		{2, "Bob", 25},
		{3, "Alice", 31}, // duplicate name
		{4, "Carol", 35},
		{5, "Bob", 26}, // duplicate name
	})

	source := ksqlite.Query(db, "SELECT id, name, age FROM users ORDER BY id", scanUser)
	deduped := source.Dedupe(func(u User) string { return u.Name })

	results, err := deduped.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 unique names, got %d: %v", len(results), results)
	}
}
