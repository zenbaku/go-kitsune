package kitsune

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jonathan/go-kitsune/internal/engine"
)

// ---------------------------------------------------------------------------
// Key & Ref — typed pipeline state
// ---------------------------------------------------------------------------

// Key identifies a piece of typed, run-scoped pipeline state.
// Declare keys as package-level variables:
//
//	var QueryOrigin = kitsune.NewKey[map[string]Item]("query-origin", make(map[string]Item))
type Key[T any] struct {
	name    string
	initial T
}

// NewKey creates a typed state key with the given name and initial value.
// The initial value is used at the start of each pipeline [Runner.Run].
func NewKey[T any](name string, initial T) Key[T] {
	return Key[T]{name: name, initial: initial}
}

// Ref provides concurrent-safe access to a piece of pipeline state.
// It is injected into stage functions by [MapWith] and [FlatMapWith].
//
// When no [Store] is configured (the default), Ref operates entirely in
// memory with a mutex — zero serialization overhead. When a Store is
// configured via [WithStore], Ref delegates to it using JSON serialization.
type Ref[T any] struct {
	mu    sync.RWMutex
	value T

	// Store-backed path (nil for memory-only).
	store Store
	key   string
}

// Get returns the current state value.
func (r *Ref[T]) Get(ctx context.Context) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.value, nil
	}
	return r.storeGet(ctx)
}

// Set overwrites the state value.
func (r *Ref[T]) Set(ctx context.Context, value T) error {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.value = value
		return nil
	}
	return r.storeSet(ctx, value)
}

// Update performs an atomic read-modify-write on the state value.
func (r *Ref[T]) Update(ctx context.Context, fn func(T) (T, error)) error {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		v, err := fn(r.value)
		if err != nil {
			return err
		}
		r.value = v
		return nil
	}
	return r.storeUpdate(ctx, fn)
}

// Store-backed operations use JSON serialization and mutex for atomicity.
func (r *Ref[T]) storeGet(ctx context.Context) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	data, ok, err := r.store.Get(ctx, r.key)
	if err != nil {
		var zero T
		return zero, err
	}
	if !ok {
		return r.value, nil // return initial value if not in store
	}
	var val T
	if err := json.Unmarshal(data, &val); err != nil {
		var zero T
		return zero, err
	}
	return val, nil
}

func (r *Ref[T]) storeSet(ctx context.Context, value T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.store.Set(ctx, r.key, data)
}

func (r *Ref[T]) storeUpdate(ctx context.Context, fn func(T) (T, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Read current.
	current := r.value
	data, ok, err := r.store.Get(ctx, r.key)
	if err != nil {
		return err
	}
	if ok {
		if err := json.Unmarshal(data, &current); err != nil {
			return err
		}
	}
	// Apply update.
	newVal, err := fn(current)
	if err != nil {
		return err
	}
	// Write back.
	data, err = json.Marshal(newVal)
	if err != nil {
		return err
	}
	return r.store.Set(ctx, r.key, data)
}

// ---------------------------------------------------------------------------
// Stateful operators
// ---------------------------------------------------------------------------

// MapWith applies a 1:1 transform with access to typed pipeline state.
func MapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) (O, error), opts ...StageOption) *Pipeline[O] {
	g := p.g
	g.RegisterKey(key.name, func(store any) any {
		ref := &Ref[S]{value: key.initial, key: key.name}
		if s, ok := store.(Store); ok && s != nil {
			ref.store = s
		}
		return ref
	})

	wrapped := func(ctx context.Context, in any) (any, error) {
		ref := g.GetRef(key.name).(*Ref[S])
		return fn(ctx, ref, in.(I))
	}
	n := newNode(engine.Map, wrapped, p, opts)
	id := g.AddNode(n)
	return &Pipeline[O]{g: g, node: id}
}

// FlatMapWith applies a 1:N transform with access to typed pipeline state.
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	g := p.g
	g.RegisterKey(key.name, func(store any) any {
		ref := &Ref[S]{value: key.initial, key: key.name}
		if s, ok := store.(Store); ok && s != nil {
			ref.store = s
		}
		return ref
	})

	wrapped := func(ctx context.Context, in any) ([]any, error) {
		ref := g.GetRef(key.name).(*Ref[S])
		results, err := fn(ctx, ref, in.(I))
		if err != nil {
			return nil, err
		}
		out := make([]any, len(results))
		for i, r := range results {
			out[i] = r
		}
		return out, nil
	}
	n := newNode(engine.FlatMap, wrapped, p, opts)
	id := g.AddNode(n)
	return &Pipeline[O]{g: g, node: id}
}

// ---------------------------------------------------------------------------
// Store — state backend
// ---------------------------------------------------------------------------

// Store is the backend interface for pipeline state persistence.
// [MemoryStore] is the default. External stores (Redis, DynamoDB) can
// implement this interface with []byte serialization.
//
// Users own connection lifecycle — create, configure, and close the
// backing client. Kitsune will never open or close connections.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

// MemoryStore returns an in-process, mutex-protected state store.
// Useful for testing or when pipeline state does not need to survive restarts.
func MemoryStore() Store {
	return &memoryStore{values: make(map[string][]byte)}
}

type memoryStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok, nil
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

// ---------------------------------------------------------------------------
// Cache — key-value cache with TTL
// ---------------------------------------------------------------------------

// Cache supports key-value caching with TTL. Use it with the [Cache] stage
// option on [Map] to skip redundant calls on repeated keys.
// External implementations (Redis, Memcached) can implement this interface.
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
// DedupSet — deduplication tracker
// ---------------------------------------------------------------------------

// DedupSet tracks seen keys for use with [Dedupe].
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
