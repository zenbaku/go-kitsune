package kitsune

import (
	"context"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/engine"
)

// ---------------------------------------------------------------------------
// Key & Ref — typed pipeline state
// ---------------------------------------------------------------------------

// KeyOption configures a Key.
type KeyOption func(*keyConfig)

type keyConfig struct {
	ttl time.Duration
}

// StateTTL sets a time-to-live for state values associated with this key.
// When a Ref's value has not been written for longer than ttl, Ref.Get
// returns the initial value and the slot is reset. Expiry is checked lazily
// on read. For Store-backed Refs, expiry is tracked on the Ref side (not
// propagated to the Store's native TTL).
func StateTTL(ttl time.Duration) KeyOption {
	return func(c *keyConfig) { c.ttl = ttl }
}

// Key identifies a piece of typed, run-scoped pipeline state.
// Declare keys as package-level variables:
//
//	var QueryOrigin = kitsune.NewKey[map[string]Item]("query-origin", make(map[string]Item))
type Key[T any] struct {
	name    string
	initial T
	ttl     time.Duration
}

// NewKey creates a typed state key with the given name and initial value.
// The initial value is used at the start of each pipeline [Runner.Run].
func NewKey[T any](name string, initial T, opts ...KeyOption) Key[T] {
	cfg := &keyConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return Key[T]{name: name, initial: initial, ttl: cfg.ttl}
}

// Ref provides concurrent-safe access to a piece of pipeline state.
// It is injected into stage functions by [MapWith] and [FlatMapWith].
//
// When no [Store] is configured (the default), Ref operates entirely in
// memory with a mutex — zero serialization overhead. When a Store is
// configured via [WithStore], Ref delegates to it using the run's [Codec]
// (defaults to JSON; override with [WithCodec]).
type Ref[T any] struct {
	mu    sync.RWMutex
	value T

	// Store-backed path (nil for memory-only).
	store Store
	key   string
	codec Codec

	// TTL fields (zero values = no TTL).
	ttl        time.Duration
	lastWrite  time.Time // zero = never written
	initialVal T
}

// Get returns the current state value.
func (r *Ref[T]) Get(ctx context.Context) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		if r.ttl > 0 && !r.lastWrite.IsZero() && time.Now().After(r.lastWrite.Add(r.ttl)) {
			r.mu.RUnlock()
			r.mu.Lock()
			defer r.mu.Unlock()
			// Double-check after lock upgrade.
			if !r.lastWrite.IsZero() && time.Now().After(r.lastWrite.Add(r.ttl)) {
				r.value = r.initialVal
				r.lastWrite = time.Time{}
			}
			return r.value, nil
		}
		v := r.value
		r.mu.RUnlock()
		return v, nil
	}
	return r.storeGet(ctx)
}

// Set overwrites the state value.
func (r *Ref[T]) Set(ctx context.Context, value T) error {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.value = value
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
		return nil
	}
	return r.storeSet(ctx, value)
}

// GetOrSet returns the current state value if it has been explicitly set, or
// calls fn to compute and store a default. For memory Refs, the in-memory
// value (including the initial value) is always returned directly without
// calling fn. For store-backed Refs, fn is called only when the key is absent
// from the store.
func (r *Ref[T]) GetOrSet(ctx context.Context, fn func() (T, error)) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.value, nil
	}
	return r.storeGetOrSet(ctx, fn)
}

// UpdateAndGet performs an atomic read-modify-write and returns the new value.
func (r *Ref[T]) UpdateAndGet(ctx context.Context, fn func(T) (T, error)) (T, error) {
	if r.store == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		v, err := fn(r.value)
		if err != nil {
			var zero T
			return zero, err
		}
		r.value = v
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
		return v, nil
	}
	return r.storeUpdateAndGet(ctx, fn)
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
		if r.ttl > 0 {
			r.lastWrite = time.Now()
		}
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
	if err := r.codec.Unmarshal(data, &val); err != nil {
		var zero T
		return zero, err
	}
	return val, nil
}

func (r *Ref[T]) storeSet(ctx context.Context, value T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := r.codec.Marshal(value)
	if err != nil {
		return err
	}
	return r.store.Set(ctx, r.key, data)
}

func (r *Ref[T]) storeGetOrSet(ctx context.Context, fn func() (T, error)) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, ok, err := r.store.Get(ctx, r.key)
	if err != nil {
		var zero T
		return zero, err
	}
	if ok {
		var val T
		if err := r.codec.Unmarshal(data, &val); err != nil {
			var zero T
			return zero, err
		}
		return val, nil
	}
	// Key absent — call fn, store the result.
	val, err := fn()
	if err != nil {
		var zero T
		return zero, err
	}
	data, err = r.codec.Marshal(val)
	if err != nil {
		var zero T
		return zero, err
	}
	if err := r.store.Set(ctx, r.key, data); err != nil {
		var zero T
		return zero, err
	}
	return val, nil
}

func (r *Ref[T]) storeUpdateAndGet(ctx context.Context, fn func(T) (T, error)) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.value
	data, ok, err := r.store.Get(ctx, r.key)
	if err != nil {
		var zero T
		return zero, err
	}
	if ok {
		if err := r.codec.Unmarshal(data, &current); err != nil {
			var zero T
			return zero, err
		}
	}
	newVal, err := fn(current)
	if err != nil {
		var zero T
		return zero, err
	}
	data, err = r.codec.Marshal(newVal)
	if err != nil {
		var zero T
		return zero, err
	}
	if err := r.store.Set(ctx, r.key, data); err != nil {
		var zero T
		return zero, err
	}
	return newVal, nil
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
		if err := r.codec.Unmarshal(data, &current); err != nil {
			return err
		}
	}
	// Apply update.
	newVal, err := fn(current)
	if err != nil {
		return err
	}
	// Write back.
	data, err = r.codec.Marshal(newVal)
	if err != nil {
		return err
	}
	return r.store.Set(ctx, r.key, data)
}

// ---------------------------------------------------------------------------
// Stateful operators
// ---------------------------------------------------------------------------

// newRef constructs a Ref with all fields set.
func newRef[S any](initial S, storeKey string, store Store, codec Codec, ttl time.Duration) *Ref[S] {
	return &Ref[S]{
		value:      initial,
		initialVal: initial,
		key:        storeKey,
		store:      store,
		codec:      codec,
		ttl:        ttl,
	}
}

// MapWith applies a 1:1 transform with access to typed pipeline state.
func MapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) (O, error), opts ...StageOption) *Pipeline[O] {
	g := p.g
	g.RegisterKey(key.name, func(store Store, codec Codec) any {
		return newRef[S](key.initial, key.name, store, codec, key.ttl)
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
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	g := p.g
	g.RegisterKey(key.name, func(store Store, codec Codec) any {
		return newRef[S](key.initial, key.name, store, codec, key.ttl)
	})

	wrapped := func(ctx context.Context, in any, yield func(any) error) error {
		ref := g.GetRef(key.name).(*Ref[S])
		return fn(ctx, ref, in.(I), func(out O) error {
			return yield(out)
		})
	}
	n := newNode(engine.FlatMap, wrapped, p, opts)
	id := g.AddNode(n)
	return &Pipeline[O]{g: g, node: id}
}

// ---------------------------------------------------------------------------
// Keyed stateful operators (per-entity state)
// ---------------------------------------------------------------------------

// keyedRefMap holds a per-entity-key Ref map. It is initialized at run time
// (like a plain Ref) and looked up by entity key on each item.
type keyedRefMap[S any] struct {
	mu      sync.Mutex
	refs    map[string]*Ref[S]
	initial S
	store   Store
	codec   Codec
	keyName string
	ttl     time.Duration
}

func (m *keyedRefMap[S]) getRef(entityKey string) *Ref[S] {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ref, ok := m.refs[entityKey]; ok {
		// If expired, evict and create fresh.
		if m.ttl > 0 && !ref.lastWrite.IsZero() && time.Now().After(ref.lastWrite.Add(m.ttl)) {
			delete(m.refs, entityKey)
		} else {
			return ref
		}
	}
	storeKey := m.keyName + ":" + entityKey
	ref := newRef[S](m.initial, storeKey, m.store, m.codec, m.ttl)
	m.refs[entityKey] = ref
	return ref
}

// MapWithKey applies a 1:1 transform with per-entity typed pipeline state.
// keyFn extracts a string entity key from each item; items with the same key
// share a Ref, while different keys get independent Refs.
func MapWithKey[I, O, S any](
	p *Pipeline[I],
	keyFn func(I) string,
	key Key[S],
	fn func(context.Context, *Ref[S], I) (O, error),
	opts ...StageOption,
) *Pipeline[O] {
	g := p.g
	regName := key.name + ":__keyed__"
	g.RegisterKey(regName, func(store Store, codec Codec) any {
		return &keyedRefMap[S]{
			refs:    make(map[string]*Ref[S]),
			initial: key.initial,
			store:   store,
			codec:   codec,
			keyName: key.name,
			ttl:     key.ttl,
		}
	})

	wrapped := func(ctx context.Context, in any) (any, error) {
		item := in.(I)
		km := g.GetRef(regName).(*keyedRefMap[S])
		ref := km.getRef(keyFn(item))
		return fn(ctx, ref, item)
	}
	n := newNode(engine.Map, wrapped, p, opts)
	id := g.AddNode(n)
	return &Pipeline[O]{g: g, node: id}
}

// FlatMapWithKey applies a 1:N transform with per-entity typed pipeline state.
func FlatMapWithKey[I, O, S any](
	p *Pipeline[I],
	keyFn func(I) string,
	key Key[S],
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	opts ...StageOption,
) *Pipeline[O] {
	g := p.g
	regName := key.name + ":__keyed__"
	g.RegisterKey(regName, func(store Store, codec Codec) any {
		return &keyedRefMap[S]{
			refs:    make(map[string]*Ref[S]),
			initial: key.initial,
			store:   store,
			codec:   codec,
			keyName: key.name,
			ttl:     key.ttl,
		}
	})

	wrapped := func(ctx context.Context, in any, yield func(any) error) error {
		item := in.(I)
		km := g.GetRef(regName).(*keyedRefMap[S])
		ref := km.getRef(keyFn(item))
		return fn(ctx, ref, item, func(out O) error {
			return yield(out)
		})
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
type Store = engine.Store

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

// Cache supports key-value caching with TTL. Use it with the [CacheBy] stage
// option on [Map] to skip redundant calls on repeated keys.
// External implementations (Redis, Memcached) can implement this interface.
type Cache = engine.Cache

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
