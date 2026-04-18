// Package kpostgres provides PostgreSQL source and sink helpers for kitsune pipelines.
//
// The caller owns the [pgxpool.Pool] and [pgx.Conn]: configure connection
// strings, pool sizes, and TLS yourself. Kitsune will never create or close
// connections.
//
// LISTEN/NOTIFY source:
//
//	conn, _ := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
//	defer conn.Close(ctx)
//
//	pipe := kpostgres.Listen[Event](conn, "events", func(payload string) (Event, error) {
//	    var e Event
//	    return e, json.Unmarshal([]byte(payload), &e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Bulk insert via COPY:
//
//	pool, _ := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
//	defer pool.Close()
//
//	sink := kpostgres.CopyFrom[Row](pool, "my_table",
//	    []string{"id", "name"},
//	    func(r Row) []any { return []any{r.ID, r.Name} },
//	)
//	kitsune.Batch(pipe, kitsune.BatchCount(500)).ForEach(sink).Run(ctx)
//
// Delivery semantics: Listen is at-most-once; notifications are not persisted
// and will not redeliver after a disconnect. Insert and CopyFrom are
// synchronous sinks; each call is committed before returning. CopyFrom is
// all-or-nothing per batch (at-least-once when combined with retries).
package kpostgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kitsune "github.com/zenbaku/go-kitsune"
)

// Listen creates a Pipeline that receives PostgreSQL LISTEN/NOTIFY notifications
// on the named channel. unmarshal converts the raw notification payload string
// into a value of type T.
// The connection is not closed when the pipeline ends; the caller owns it.
func Listen[T any](conn *pgx.Conn, channel string, unmarshal func(payload string) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			return err
		}
		for {
			notif, err := conn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil // context cancelled: clean exit
				}
				return err
			}
			v, err := unmarshal(notif.Payload)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil
			}
		}
	})
}

// Insert returns a sink function that inserts one row per item using a
// parameterised INSERT statement. columns must match the placeholders in sql.
// Use with [kitsune.Pipeline.ForEach].
//
//	sink := kpostgres.Insert[Event](pool,
//	    "INSERT INTO events (id, payload) VALUES ($1, $2)",
//	    func(e Event) []any { return []any{e.ID, e.Payload} },
//	)
func Insert[T any](pool *pgxpool.Pool, sql string, args func(T) []any) func(context.Context, T) error {
	return func(ctx context.Context, item T) error {
		_, err := pool.Exec(ctx, sql, args(item)...)
		return err
	}
}

// CopyFrom returns a batch-sink function that uses the PostgreSQL COPY protocol
// to bulk-insert rows. rows is the function used with [kitsune.Batch] output.
// Use with [kitsune.Pipeline.ForEach] on a batch stage.
//
//	sink := kpostgres.CopyFrom[Row](pool, "my_table",
//	    []string{"id", "name"},
//	    func(r Row) []any { return []any{r.ID, r.Name} },
//	)
//	kitsune.Batch(pipe, kitsune.BatchCount(500)).ForEach(sink).Run(ctx)
func CopyFrom[T any](pool *pgxpool.Pool, table string, columns []string, row func(T) []any) func(context.Context, []T) error {
	pgColumns := make(pgx.Identifier, len(columns))
	copy(pgColumns, columns)
	return func(ctx context.Context, batch []T) error {
		rows := make([][]any, len(batch))
		for i, item := range batch {
			rows[i] = row(item)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		_, err = conn.Conn().CopyFrom(
			ctx,
			pgx.Identifier{table},
			columns,
			pgx.CopyFromRows(rows),
		)
		return err
	}
}
