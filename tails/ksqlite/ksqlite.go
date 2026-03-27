// Package ksqlite provides SQLite source and sink helpers for kitsune pipelines.
//
// Users own the [sql.DB] lifecycle — create, configure, and close it yourself.
// Kitsune will never open or close connections.
package ksqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	kitsune "github.com/jonathan/go-kitsune"
	_ "modernc.org/sqlite" // register sqlite driver
)

// ---------------------------------------------------------------------------
// Source — read rows from a query
// ---------------------------------------------------------------------------

// Query creates a Pipeline that executes the given SQL query and emits one
// item per row. The scan function converts each row into a typed value.
//
//	ksqlite.Query(db, "SELECT id, name FROM users", func(rows *sql.Rows) (User, error) {
//	    var u User
//	    err := rows.Scan(&u.ID, &u.Name)
//	    return u, err
//	})
func Query[T any](db *sql.DB, query string, scan func(*sql.Rows) (T, error), args ...any) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return err
			}
			if !yield(item) {
				return nil
			}
		}
		return rows.Err()
	})
}

// ---------------------------------------------------------------------------
// Sink — insert rows
// ---------------------------------------------------------------------------

// Insert returns a sink function that inserts each item into the given table.
// The columns function extracts column values from the item in the order
// matching the provided column names.
//
//	ksqlite.Insert(db, "users", []string{"id", "name"}, func(u User) []any {
//	    return []any{u.ID, u.Name}
//	})
func Insert[T any](db *sql.DB, table string, columns []string, values func(T) []any) func(context.Context, T) error {
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	return func(ctx context.Context, item T) error {
		_, err := db.ExecContext(ctx, query, values(item)...)
		return err
	}
}

// BatchInsert returns a sink function for batched inserts. Use with
// [kitsune.Batch] to group items into slices first.
//
//	batched := kitsune.Batch(items, 100)
//	batched.ForEach(ksqlite.BatchInsert(db, "users", cols, valsFn))
func BatchInsert[T any](db *sql.DB, table string, columns []string, values func(T) []any) func(context.Context, []T) error {
	return func(ctx context.Context, batch []T) error {
		if len(batch) == 0 {
			return nil
		}

		placeholders := make([]string, len(columns))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		rowPlaceholder := "(" + strings.Join(placeholders, ", ") + ")"

		rows := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(columns))
		for i, item := range batch {
			rows[i] = rowPlaceholder
			args = append(args, values(item)...)
		}

		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			table,
			strings.Join(columns, ", "),
			strings.Join(rows, ", "),
		)

		_, err := db.ExecContext(ctx, query, args...)
		return err
	}
}
