// Package kclickhouse provides ClickHouse source and sink helpers for kitsune
// pipelines.
//
// The caller owns the [driver.Conn]: configure DSN, auth, and pool settings
// yourself. Kitsune will never create or close connections.
//
// Stream query results:
//
//	conn, _ := clickhouse.Open(&clickhouse.Options{Addr: []string{"localhost:9000"}})
//	defer conn.Close()
//
//	pipe := kclickhouse.Query(conn, "SELECT id, name FROM events WHERE status = ?",
//	    func(rows driver.Rows) (Event, error) {
//	        var e Event
//	        return e, rows.Scan(&e.ID, &e.Name)
//	    }, "active")
//	pipe.ForEach(handle).Run(ctx)
//
// Batch insert (native protocol, much faster than individual INSERTs):
//
//	kitsune.Batch(pipe, 1000).
//	    ForEach(kclickhouse.Insert(conn, "events", func(e Event) []any {
//	        return []any{e.ID, e.Name, e.Timestamp}
//	    })).Run(ctx)
//
// Delivery semantics: Query is a read-only source with no ack mechanism
// (at-most-once from the pipeline's perspective). Insert uses the native batch
// protocol; a batch is either fully committed or returns an error. There is no
// partial-batch recovery, so treat Insert as at-least-once when combined with
// retries.
package kclickhouse

import (
	"context"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Query creates a Pipeline that streams rows from a ClickHouse query. scan is
// called once per row to convert the row into a value of type T.
//
// The connection is not closed when the pipeline ends; the caller owns it.
func Query[T any](conn driver.Conn, query string, scan func(driver.Rows) (T, error), args ...any) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			v, err := scan(rows)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil
			}
		}
		return rows.Err()
	})
}

// Insert returns a batch sink function that inserts items into a ClickHouse
// table using the native batch protocol. marshal converts each item into a
// slice of column values in the same order as the table columns.
// Use with [kitsune.Pipeline.ForEach] after [kitsune.Batch].
//
// The connection is not closed when the pipeline ends; the caller owns it.
func Insert[T any](conn driver.Conn, table string, marshal func(T) []any) func(context.Context, []T) error {
	return func(ctx context.Context, items []T) error {
		batch, err := conn.PrepareBatch(ctx, "INSERT INTO "+table)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := batch.Append(marshal(item)...); err != nil {
				return err
			}
		}
		return batch.Send()
	}
}
