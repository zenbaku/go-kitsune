package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

// TestFileDevStore_RoundTrip verifies that items saved under a segment are
// returned verbatim by a subsequent Load.
func TestFileDevStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	ctx := context.Background()

	items := []json.RawMessage{
		json.RawMessage(`{"id":1}`),
		json.RawMessage(`{"id":2}`),
		json.RawMessage(`{"id":3}`),
	}
	if err := store.Save(ctx, "seg", items); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(ctx, "seg")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(items) {
		t.Fatalf("got %d items, want %d", len(got), len(items))
	}
	for i, raw := range got {
		if string(raw) != string(items[i]) {
			t.Errorf("item %d: got %q, want %q", i, raw, items[i])
		}
	}
}

// TestFileDevStore_LoadMissing verifies that Load on an unknown segment
// returns ErrSnapshotMissing wrapping the segment name.
func TestFileDevStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	_, err := store.Load(context.Background(), "absent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, kitsune.ErrSnapshotMissing) {
		t.Errorf("err=%v, want errors.Is(err, ErrSnapshotMissing)", err)
	}
	if !contains(err.Error(), "absent") {
		t.Errorf("err=%v, want to contain segment name", err)
	}
}

// TestFileDevStore_CreatesDir verifies that Save creates the directory if
// it does not exist.
func TestFileDevStore_CreatesDir(t *testing.T) {
	parent := t.TempDir()
	nested := filepath.Join(parent, "deeply", "nested")
	store := kitsune.NewFileDevStore(nested)
	if err := store.Save(context.Background(), "x", []json.RawMessage{json.RawMessage(`1`)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "x.json")); err != nil {
		t.Errorf("expected x.json to exist: %v", err)
	}
}

// TestFileDevStore_Overwrite verifies that re-Saving the same segment
// replaces the previous snapshot.
func TestFileDevStore_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	ctx := context.Background()

	if err := store.Save(ctx, "seg", []json.RawMessage{json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "seg", []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`3`)}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, "seg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != "2" || string(got[1]) != "3" {
		t.Errorf("got %v, want [2 3]", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
