package internal

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// TTLDedupSet
// ---------------------------------------------------------------------------

// TTLDedupSet returns an in-process deduplication set that forgets keys after
// ttl has elapsed since they were last added. Memory is bounded by the set of
// currently non-expired keys. Eviction is lazy: expired entries are purged on
// the next Contains or Add call; there is no background goroutine.
//
// Re-adding an existing key refreshes its expiry (touch semantics).
// Panics if ttl <= 0.
func TTLDedupSet(ttl time.Duration) DedupSet {
	return TTLDedupSetWithClock(ttl, time.Now)
}

// TTLDedupSetWithClock is like TTLDedupSet but accepts an injectable clock
// function for deterministic testing.
func TTLDedupSetWithClock(ttl time.Duration, nowFn func() time.Time) DedupSet {
	if ttl <= 0 {
		panic(fmt.Sprintf("kitsune: TTLDedupSet: ttl must be > 0, got %v", ttl))
	}
	return &ttlDedupSet{
		ttl:     ttl,
		entries: make(map[string]time.Time),
		nowFn:   nowFn,
	}
}

type ttlEntry struct {
	key       string
	expiresAt time.Time
}

type ttlDedupSet struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time // key -> authoritative expiresAt
	queue   []ttlEntry           // FIFO queue; monotonically non-decreasing expiresAt
	head    int                  // read cursor into queue
	nowFn   func() time.Time
}

func (s *ttlDedupSet) Contains(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(s.nowFn())
	_, ok := s.entries[key]
	return ok, nil
}

func (s *ttlDedupSet) Add(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	s.evictLocked(now)
	exp := now.Add(s.ttl)
	s.entries[key] = exp
	s.queue = append(s.queue, ttlEntry{key: key, expiresAt: exp})
	return nil
}

// evictLocked removes expired entries from the head of the queue. Must be
// called with s.mu held.
func (s *ttlDedupSet) evictLocked(now time.Time) {
	for s.head < len(s.queue) {
		head := s.queue[s.head]
		if head.expiresAt.After(now) {
			break
		}
		s.head++
		// Only delete from the map if the stored expiry matches this queue entry.
		// A re-Add pushes a new entry with a later expiry; the old entry is a
		// tombstone and should not evict the refreshed key.
		if exp, ok := s.entries[head.key]; ok && exp.Equal(head.expiresAt) {
			delete(s.entries, head.key)
		}
	}
	// Compact: reclaim the dead prefix when it is at least half the slice.
	if s.head > 0 && s.head >= len(s.queue)/2 {
		n := copy(s.queue, s.queue[s.head:])
		s.queue = s.queue[:n]
		s.head = 0
	}
}
