package kclickhouse_test

import (
	"context"
	"os"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	kitsune "github.com/jonathan/go-kitsune"
	"github.com/jonathan/go-kitsune/tails/kclickhouse"
)

func skipIfNoClickHouse(t *testing.T) driver.Conn {
	t.Helper()
	addr := os.Getenv("CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_ADDR not set; skipping ClickHouse integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}})
	if err != nil {
		t.Skipf("clickhouse.Open: %v", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		t.Skipf("clickhouse ping: %v", err)
	}
	return conn
}

func TestQueryAndInsert(t *testing.T) {
	conn := skipIfNoClickHouse(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a test table.
	tableName := "kclickhouse_test_" + t.Name()
	err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+tableName+
		" (n Int32) ENGINE = Memory")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName)
	})

	// Insert 5 rows.
	items := []int32{10, 20, 30, 40, 50}
	insertSink := kclickhouse.Insert(conn, tableName, func(n int32) []any { return []any{n} })
	if err := insertSink(ctx, items); err != nil {
		t.Fatal(err)
	}

	// Query them back.
	got, err := kclickhouse.Query(conn, "SELECT n FROM "+tableName+" ORDER BY n",
		func(rows driver.Rows) (int32, error) {
			var n int32
			return n, rows.Scan(&n)
		},
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 rows, got %d", len(got))
	}
}

func TestQueryTake(t *testing.T) {
	conn := skipIfNoClickHouse(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "kclickhouse_take_" + t.Name()
	err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+tableName+
		" (n Int32) ENGINE = Memory")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName)
	})

	items := []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	insertSink := kclickhouse.Insert(conn, tableName, func(n int32) []any { return []any{n} })
	if err := insertSink(ctx, items); err != nil {
		t.Fatal(err)
	}

	got, err := kitsune.Map(
		kclickhouse.Query(conn, "SELECT n FROM "+tableName+" ORDER BY n",
			func(rows driver.Rows) (int32, error) {
				var n int32
				return n, rows.Scan(&n)
			},
		),
		func(_ context.Context, n int32) (int32, error) { return n, nil },
	).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}
