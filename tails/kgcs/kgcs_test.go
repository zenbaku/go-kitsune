package kgcs_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kgcs"
)

// ---------------------------------------------------------------------------
// In-memory stub that satisfies kgcs.GCSClient
// ---------------------------------------------------------------------------

type stubClient struct {
	objects map[string]string // key → body
	written map[string]string // key → body written via Upload
}

func (c *stubClient) ListObjects(_ context.Context, _, prefix string) kgcs.ObjectIterator {
	var names []string
	for k := range c.objects {
		if strings.HasPrefix(k, prefix) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return &stubIterator{names: names}
}

func (c *stubClient) NewReader(_ context.Context, _, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(c.objects[key])), nil
}

func (c *stubClient) NewWriter(_ context.Context, _, key string) (io.WriteCloser, error) {
	return &stubWriter{c: c, key: key, buf: new(bytes.Buffer)}, nil
}

type stubIterator struct {
	names []string
	i     int
}

func (it *stubIterator) Next() (string, error) {
	if it.i >= len(it.names) {
		return "", kgcs.Done
	}
	name := it.names[it.i]
	it.i++
	return name, nil
}

type stubWriter struct {
	c   *stubClient
	key string
	buf *bytes.Buffer
}

func (w *stubWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *stubWriter) Close() error {
	if w.c.written == nil {
		w.c.written = make(map[string]string)
	}
	w.c.written[w.key] = w.buf.String()
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestListObjects(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"data/a.txt":  "hello",
			"data/b.txt":  "world",
			"other/c.txt": "skip",
		},
	}

	results, err := kgcs.ListObjects[string](client, "bucket", "data/",
		func(_ string, body io.Reader) (string, error) {
			b, err := io.ReadAll(body)
			return string(b), err
		},
	).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 objects, got %d: %v", len(results), results)
	}
	// stub iterator returns sorted names, so order is deterministic
	if results[0] != "hello" || results[1] != "world" {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestListObjectsPrefixFilter(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"a/1.txt": "one",
			"a/2.txt": "two",
			"b/3.txt": "three",
		},
	}

	results, err := kgcs.ListObjects[string](client, "bucket", "b/",
		func(_ string, body io.Reader) (string, error) {
			b, _ := io.ReadAll(body)
			return string(b), nil
		},
	).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0] != "three" {
		t.Errorf("expected [three], got %v", results)
	}
}

func TestListObjectsEmpty(t *testing.T) {
	client := &stubClient{objects: map[string]string{}}

	results, err := kgcs.ListObjects[string](client, "bucket", "prefix/",
		func(_ string, body io.Reader) (string, error) {
			b, _ := io.ReadAll(body)
			return string(b), nil
		},
	).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %v", results)
	}
}

func TestListObjectsComposable(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"p/x.txt": "a",
			"p/y.txt": "b",
			"p/z.txt": "c",
		},
	}

	pipe := kgcs.ListObjects[string](client, "bucket", "p/",
		func(_ string, body io.Reader) (string, error) {
			b, _ := io.ReadAll(body)
			return string(b), nil
		},
	)
	results, err := kitsune.Map(pipe, func(_ context.Context, s string) (string, error) {
		return strings.ToUpper(s), nil
	}).Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d: %v", len(results), results)
	}
	for _, r := range results {
		if r != strings.ToUpper(r) {
			t.Errorf("expected uppercase, got %q", r)
		}
	}
}

func TestLines(t *testing.T) {
	client := &stubClient{
		objects: map[string]string{
			"file.txt": "line1\nline2\nline3",
		},
	}

	results, err := kgcs.Lines(client, "bucket", "file.txt").
		Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"line1", "line2", "line3"}
	if len(results) != len(want) {
		t.Fatalf("expected %d lines, got %d: %v", len(want), len(results), results)
	}
	for i, w := range want {
		if results[i] != w {
			t.Errorf("[%d] got %q, want %q", i, results[i], w)
		}
	}
}

func TestLinesEmpty(t *testing.T) {
	client := &stubClient{objects: map[string]string{"empty.txt": ""}}

	results, err := kgcs.Lines(client, "bucket", "empty.txt").
		Collect(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %v", results)
	}
}

func TestUpload(t *testing.T) {
	client := &stubClient{}

	type record struct{ ID, Value string }
	items := []record{
		{"a", "hello"},
		{"b", "world"},
	}

	_, err := kitsune.FromSlice(items).ForEach(
		kgcs.Upload(client, "bucket",
			func(r record) string { return "out/" + r.ID + ".txt" },
			func(r record, w io.Writer) error {
				_, err := io.WriteString(w, r.Value)
				return err
			},
		),
	).Run(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.written) != 2 {
		t.Fatalf("expected 2 uploads, got %d: %v", len(client.written), client.written)
	}
	if client.written["out/a.txt"] != "hello" {
		t.Errorf("out/a.txt: got %q, want %q", client.written["out/a.txt"], "hello")
	}
	if client.written["out/b.txt"] != "world" {
		t.Errorf("out/b.txt: got %q, want %q", client.written["out/b.txt"], "world")
	}
}

func TestUploadKeyFunction(t *testing.T) {
	client := &stubClient{}

	_, err := kitsune.FromSlice([]int{1, 2, 3}).ForEach(
		kgcs.Upload(client, "bucket",
			func(n int) string { return fmt.Sprintf("items/%04d.txt", n) },
			func(n int, w io.Writer) error {
				_, err := fmt.Fprintf(w, "%d", n)
				return err
			},
		),
	).Run(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.written["items/0001.txt"] != "1" {
		t.Errorf("items/0001.txt: got %q", client.written["items/0001.txt"])
	}
}
