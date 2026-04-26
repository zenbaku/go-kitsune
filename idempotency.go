package kitsune

import (
	"context"
	"sync"
)

// IdempotencyStore tracks idempotency keys for [Effect] dedupe.
// Implementations must be safe for concurrent use; Add must record the key
// atomically so two concurrent Adds with the same key see exactly one
// firstTime=true return.
//
// The default in-process implementation is attached automatically to an
// [Effect] stage when [EffectPolicy.Idempotent] is true and
// [EffectPolicy.IdempotencyKey] is non-nil. To dedupe across runs (so a
// key recorded in one run is honoured by the next), supply a persistent
// implementation via [EffectPolicy.IdempotencyStore], for example a thin
// wrapper around Redis SETNX.
type IdempotencyStore interface {
	// Add atomically records key as seen. It returns true if key was
	// newly recorded (first time seen) and false if the key was already
	// present. A non-nil error indicates the store could not be queried;
	// the calling Effect treats this as a per-item failure.
	Add(ctx context.Context, key string) (firstTime bool, err error)
}

// memoryIdempotencyStore is the default in-process [IdempotencyStore].
// One is constructed per Effect stage when no user-supplied store is
// configured; its lifetime is the stage goroutine's lifetime (one Run).
type memoryIdempotencyStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
	return &memoryIdempotencyStore{seen: make(map[string]struct{})}
}

// Add implements [IdempotencyStore].
func (s *memoryIdempotencyStore) Add(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[key]; ok {
		return false, nil
	}
	s.seen[key] = struct{}{}
	return true, nil
}
