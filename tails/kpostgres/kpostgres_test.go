package kpostgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kpostgres"
)

// dsn returns the test database URL from the environment,
// or skips the test if it is not configured.
func dsn(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	return url
}

func TestInsert(t *testing.T) {
	url := dsn(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE kpostgres_test (id int, val text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	type Row struct {
		ID  int
		Val string
	}

	sink := kpostgres.Insert[Row](pool,
		"INSERT INTO kpostgres_test (id, val) VALUES ($1, $2)",
		func(r Row) []any { return []any{r.ID, r.Val} },
	)

	rows := []Row{{1, "a"}, {2, "b"}, {3, "c"}}
	if err := kitsune.FromSlice(rows).ForEach(sink).Run(ctx); err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM kpostgres_test").Scan(&count) //nolint:errcheck
	if count != 3 {
		t.Fatalf("expected 3 rows, got %d", count)
	}
}

func TestCopyFrom(t *testing.T) {
	url := dsn(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE kpostgres_copy_test (id int, val text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	type Row struct {
		ID  int
		Val string
	}

	sink := kpostgres.CopyFrom[Row](pool, "kpostgres_copy_test",
		[]string{"id", "val"},
		func(r Row) []any { return []any{r.ID, r.Val} },
	)

	rows := make([]Row, 100)
	for i := range rows {
		rows[i] = Row{i, "v"}
	}
	if err := kitsune.Batch(kitsune.FromSlice(rows), 25).ForEach(sink).Run(ctx); err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM kpostgres_copy_test").Scan(&count) //nolint:errcheck
	if count != 100 {
		t.Fatalf("expected 100 rows, got %d", count)
	}
}

func TestListen(t *testing.T) {
	url := dsn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listener connection.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer conn.Close(ctx)

	// Notifier connection.
	notifier, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer notifier.Close(ctx)

	type Event struct{ Payload string }
	pipe := kpostgres.Listen[Event](conn, "test_channel", func(p string) (Event, error) {
		return Event{p}, nil
	})

	received := make(chan Event, 3)
	go func() {
		pipe.Take(3).ForEach(func(_ context.Context, e Event) error {
			received <- e
			return nil
		}).Run(ctx) //nolint:errcheck
	}()

	// Send three notifications.
	for _, payload := range []string{"a", "b", "c"} {
		notifier.Exec(ctx, "SELECT pg_notify('test_channel', $1)", payload) //nolint:errcheck
	}

	var got []Event
	for range 3 {
		select {
		case e := <-received:
			got = append(got, e)
		case <-ctx.Done():
			t.Fatal("timeout waiting for notifications")
		}
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
}
