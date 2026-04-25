package kitsune_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/zenbaku/go-kitsune"
)

type checkpointItem struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TestFromCheckpoint_LoadsStoredSnapshot verifies that FromCheckpoint emits
// the items previously persisted by Save.
func TestFromCheckpoint_LoadsStoredSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	raws := []json.RawMessage{
		json.RawMessage(`{"id":1,"name":"a"}`),
		json.RawMessage(`{"id":2,"name":"b"}`),
	}
	if err := store.Save(context.Background(), "items", raws); err != nil {
		t.Fatal(err)
	}

	p := kitsune.FromCheckpoint[checkpointItem](store, "items")
	got, err := kitsune.Collect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []checkpointItem{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestFromCheckpoint_MissingErrors verifies that running FromCheckpoint with
// no stored snapshot returns an error wrapping ErrSnapshotMissing.
func TestFromCheckpoint_MissingErrors(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)

	p := kitsune.FromCheckpoint[checkpointItem](store, "absent")
	_, err := kitsune.Collect(context.Background(), p)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, kitsune.ErrSnapshotMissing) {
		t.Errorf("err=%v, want errors.Is(err, ErrSnapshotMissing)", err)
	}
}

// TestFromCheckpoint_WithName verifies that WithName is honoured by
// FromCheckpoint's stage metadata.
func TestFromCheckpoint_WithName(t *testing.T) {
	dir := t.TempDir()
	store := kitsune.NewFileDevStore(dir)
	if err := store.Save(context.Background(), "x", []json.RawMessage{json.RawMessage(`0`)}); err != nil {
		t.Fatal(err)
	}
	p := kitsune.FromCheckpoint[int](store, "x", kitsune.WithName("custom"))
	nodes := p.Describe()
	var found bool
	for _, n := range nodes {
		if n.Name == "custom" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected stage named %q in graph, got %+v", "custom", nodes)
	}
}
