package kitsune

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DevStore persists per-segment snapshots for development-time replay.
//
// It is a strictly development-only affordance. Snapshots may become stale
// silently when segment outputs change shape; there is no schema versioning,
// no concurrency safety beyond what implementations choose to provide, and
// no production guarantees. Attaching a DevStore to a production run is an
// explicit choice by the author.
//
// See [WithDevStore] for the run-level integration and [FromCheckpoint] for
// loading a snapshot as a pipeline source.
type DevStore interface {
	// Save persists items under segment as a JSON-array-shaped snapshot.
	// Implementations should overwrite any existing snapshot for segment.
	// The items slice is owned by the caller for the duration of Save and
	// should not be retained after Save returns.
	Save(ctx context.Context, segment string, items []json.RawMessage) error

	// Load returns the snapshot previously persisted under segment. If no
	// snapshot exists, Load returns ErrSnapshotMissing wrapped with the
	// segment name; callers should distinguish missing from other errors
	// via errors.Is.
	Load(ctx context.Context, segment string) ([]json.RawMessage, error)
}

// ErrSnapshotMissing is returned by [DevStore.Load] when no snapshot exists
// for the requested segment.
var ErrSnapshotMissing = fmt.Errorf("kitsune: devstore snapshot missing")

// FileDevStore writes one JSON file per segment under a configurable
// directory. The directory is created on first Save if it does not exist.
// A single FileDevStore is safe for concurrent Save and Load calls under
// distinct segment names; concurrent Save calls on the same segment have
// last-write-wins semantics.
type FileDevStore struct {
	dir string
	mu  sync.Mutex // serialises writes to a single file at a time
}

// NewFileDevStore returns a [FileDevStore] rooted at dir. The directory is
// created on first Save if it does not exist. dir is interpreted relative
// to the current working directory unless it is absolute.
func NewFileDevStore(dir string) *FileDevStore {
	return &FileDevStore{dir: dir}
}

// segmentPath returns the on-disk path for a segment's snapshot file.
func (s *FileDevStore) segmentPath(segment string) string {
	return filepath.Join(s.dir, segment+".json")
}

// Save persists items as a JSON array under segment.json in the store
// directory. The directory is created if it does not exist.
func (s *FileDevStore) Save(_ context.Context, segment string, items []json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("kitsune: devstore mkdir %q: %w", s.dir, err)
	}
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("kitsune: devstore marshal segment %q: %w", segment, err)
	}
	if err := os.WriteFile(s.segmentPath(segment), data, 0o644); err != nil {
		return fmt.Errorf("kitsune: devstore write segment %q: %w", segment, err)
	}
	return nil
}

// Load returns the snapshot persisted under segment.json, or wraps
// [ErrSnapshotMissing] if the file does not exist.
func (s *FileDevStore) Load(_ context.Context, segment string) ([]json.RawMessage, error) {
	data, err := os.ReadFile(s.segmentPath(segment))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: segment %q", ErrSnapshotMissing, segment)
		}
		return nil, fmt.Errorf("kitsune: devstore read segment %q: %w", segment, err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("kitsune: devstore unmarshal segment %q: %w", segment, err)
	}
	return items, nil
}
