package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	kithooks "github.com/zenbaku/go-kitsune/hooks"
)

// ---------------------------------------------------------------------------
// Hook interfaces — aliases from github.com/zenbaku/go-kitsune/hooks
//
// Using type aliases (=) means internal.Hook IS hooks.Hook — a tail that
// implements hooks.Hook satisfies internal.Hook with no conversion needed.
// Swapping the engine is a one-line go.mod change for user code.
// ---------------------------------------------------------------------------

type Hook = kithooks.Hook
type NoopHook = kithooks.NoopHook
type OverflowHook = kithooks.OverflowHook
type SupervisionHook = kithooks.SupervisionHook
type SampleHook = kithooks.SampleHook
type GraphHook = kithooks.GraphHook
type BufferHook = kithooks.BufferHook
type GraphNode = kithooks.GraphNode
type BufferStatus = kithooks.BufferStatus

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

// ErrorHandler decides what to do when a stage function returns an error.
type ErrorHandler interface {
	Handle(err error, attempt int) ErrorAction
	Backoff() func(attempt int) time.Duration
}

// ErrorAction is the decision returned by an [ErrorHandler].
type ErrorAction int

const (
	ActionHalt   ErrorAction = iota // stop the pipeline
	ActionSkip                      // drop item, continue
	ActionRetry                     // retry with backoff
	ActionReturn                    // emit replacement value, continue
)

// Returner is optionally implemented by an ErrorHandler that uses ActionReturn.
// When Handle returns ActionReturn, the engine calls ReturnValue to obtain
// the replacement item.
type Returner interface {
	ReturnValue() any
}

// DefaultHandler halts on any error.
type DefaultHandler struct{}

func (DefaultHandler) Handle(error, int) ErrorAction            { return ActionHalt }
func (DefaultHandler) Backoff() func(attempt int) time.Duration { return nil }

// ErrSkipped is an internal sentinel indicating an item was dropped.
var ErrSkipped = errors.New("kitsune: item skipped")

// StageError wraps an error with the originating stage name and attempt number.
type StageError struct {
	Stage   string
	Attempt int
	Cause   error
}

func (e *StageError) Error() string {
	if e.Attempt > 0 {
		return fmt.Sprintf("stage %q: attempt %d: %v", e.Stage, e.Attempt, e.Cause)
	}
	return fmt.Sprintf("stage %q: %v", e.Stage, e.Cause)
}

func (e *StageError) Unwrap() error { return e.Cause }

// ---------------------------------------------------------------------------
// Overflow
// ---------------------------------------------------------------------------

// DefaultBuffer is the default channel buffer size between stages.
const DefaultBuffer = 16

// Overflow is the strategy used when a stage's output buffer is full.
type Overflow int

const (
	OverflowBlock      Overflow = iota // block until space is available (default)
	OverflowDropNewest                 // discard the incoming item when the buffer is full
	OverflowDropOldest                 // evict the oldest buffered item to make room
)

// ---------------------------------------------------------------------------
// Supervision
// ---------------------------------------------------------------------------

// SupervisionPolicy configures per-stage restart and panic-recovery behavior.
// Zero value = no restarts, panics propagate (identical to v1 behavior).
type SupervisionPolicy struct {
	MaxRestarts int                     // 0 = no restarts (default)
	Window      time.Duration           // reset counter after quiet period; 0 = never reset
	Backoff     func(int) time.Duration // delay between restarts (nil = no delay)
	OnPanic     PanicAction
	PanicOnly   bool // when true, only restart on panics; regular errors halt immediately
}

// HasSupervision reports whether any supervision is active.
func (p SupervisionPolicy) HasSupervision() bool {
	return p.MaxRestarts > 0 || p.OnPanic != PanicPropagate
}

// PanicAction configures what happens when a stage goroutine panics.
type PanicAction int

const (
	PanicPropagate PanicAction = iota // re-panic (default — existing behavior)
	PanicRestart                      // treat panic as a restartable error
	PanicSkip                         // recover and continue; the panicking item is lost
)

// ---------------------------------------------------------------------------
// Codec
// ---------------------------------------------------------------------------

// Codec serialises and deserialises values for [Store]-backed state and
// [CacheBy] stages. The default implementation uses [encoding/json].
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// JSONCodec is the default [Codec] implementation backed by [encoding/json].
type JSONCodec struct{}

func (JSONCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (JSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store is the backend interface for pipeline state persistence.
// Implementations must be safe for concurrent use.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

// MemoryStore returns an in-process, mutex-protected state store.
// Useful for testing or when pipeline state does not need to survive restarts.
func MemoryStore() Store {
	return &memoryStore{values: make(map[string]any)}
}

type memoryStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	if !ok {
		return nil, false, nil
	}
	// Values written via Store.Set are stored as []byte; return directly.
	if b, isByteSlice := v.([]byte); isByteSlice {
		return b, true, nil
	}
	// Values written via SetAny are typed — marshal for external callers.
	data, err := json.Marshal(v)
	return data, true, err
}

func (s *memoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// InProcessStore is implemented by stores that live in the same process and
// support direct any-typed access, bypassing codec serialization.
// [MemoryStore] implements this interface.
type InProcessStore interface {
	GetAny(key string) (any, bool)
	SetAny(key string, value any)
	DeleteAny(key string)
}

// InProcessStore implementation — no error returns; in-process maps cannot fail.

func (s *memoryStore) GetAny(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

func (s *memoryStore) SetAny(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *memoryStore) DeleteAny(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

// Cache supports key-value caching with TTL.
// Implementations must be safe for concurrent use.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// MemoryCache returns an in-process cache with a maximum number of entries.
// When full, the oldest entry is evicted. TTL is respected on reads.
func MemoryCache(maxSize int) Cache {
	return &memoryCache{
		maxSize: maxSize,
		entries: make(map[string]cacheEntry),
	}
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

type memoryCache struct {
	mu      sync.RWMutex
	maxSize int
	entries map[string]cacheEntry
	order   []string // insertion order for eviction
}

func (c *memoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		return nil, false, nil // expired
	}
	return entry.data, true, nil
}

func (c *memoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict oldest if at capacity and key is new.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxSize {
		c.evictOldest()
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = cacheEntry{data: value, expiresAt: exp}
	return nil
}

func (c *memoryCache) evictOldest() {
	for len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, ok := c.entries[oldest]; ok {
			delete(c.entries, oldest)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// DedupSet
// ---------------------------------------------------------------------------

// DedupSet tracks seen keys for use with Dedupe stages.
// External implementations (Redis SETNX, Bloom filters) can implement this interface.
type DedupSet interface {
	Contains(ctx context.Context, key string) (bool, error)
	Add(ctx context.Context, key string) error
}

// MemoryDedupSet returns an in-process deduplication set.
func MemoryDedupSet() DedupSet {
	return &memoryDedupSet{seen: make(map[string]struct{})}
}

type memoryDedupSet struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

func (s *memoryDedupSet) Contains(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.seen[key]
	return ok, nil
}

func (s *memoryDedupSet) Add(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[key] = struct{}{}
	return nil
}
