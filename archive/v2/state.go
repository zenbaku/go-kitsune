package kitsune

import (
	"context"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/v2/internal"
)

// ---------------------------------------------------------------------------
// Key & Ref — typed pipeline state
// ---------------------------------------------------------------------------

// KeyOption configures a [Key].
type KeyOption func(*keyConfig)

type keyConfig struct {
	ttl time.Duration
}

// StateTTL sets a time-to-live for state values associated with this key.
// When a Ref's value has not been written for longer than ttl, [Ref.Get]
// returns the initial value and the slot is reset. Expiry is checked lazily
// on read. For Store-backed Refs, expiry is tracked on the Ref side.
func StateTTL(ttl time.Duration) KeyOption {
	return func(c *keyConfig) { c.ttl = ttl }
}

// Key identifies a piece of typed, run-scoped pipeline state.
// Declare keys as package-level variables:
//
//	var counterKey = kitsune.NewKey[int]("counter", 0)
type Key[T any] struct {
	name    string
	initial T
	ttl     time.Duration
}

// NewKey creates a typed state key with the given name and initial value.
// The initial value is used at the start of each [Runner.Run].
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
	store internal.Store
	key   string
	codec internal.Codec

	// TTL fields (zero values = no TTL).
	ttl        time.Duration
	lastWrite  time.Time
	initialVal T
}

// Get returns the current state value. If a TTL is set and has elapsed since
// the last write, the initial value is returned and the slot is reset.
func (r *Ref[T]) Get(ctx context.Context) (T, error) {
	if r.store == nil {
		r.mu.RLock()
		if r.ttl > 0 && !r.lastWrite.IsZero() && time.Now().After(r.lastWrite.Add(r.ttl)) {
			r.mu.RUnlock()
			r.mu.Lock()
			defer r.mu.Unlock()
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

// GetOrSet returns the current value. For memory-backed Refs the in-memory
// value (including the initial) is always returned directly. For store-backed
// Refs, fn is called only when the key is absent from the store.
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

func (r *Ref[T]) storeGet(ctx context.Context) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	data, ok, err := r.store.Get(ctx, r.key)
	if err != nil {
		var zero T
		return zero, err
	}
	if !ok {
		return r.value, nil
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
	newVal, err := fn(current)
	if err != nil {
		return err
	}
	data, err = r.codec.Marshal(newVal)
	if err != nil {
		return err
	}
	return r.store.Set(ctx, r.key, data)
}

// newRef constructs a Ref with all fields set.
func newRef[S any](initial S, storeKey string, store internal.Store, codec internal.Codec, ttl time.Duration) *Ref[S] {
	return &Ref[S]{
		value:      initial,
		initialVal: initial,
		key:        storeKey,
		store:      store,
		codec:      codec,
		ttl:        ttl,
	}
}

// ---------------------------------------------------------------------------
// Stateful operators
// ---------------------------------------------------------------------------

// MapWith applies a 1:1 transform with access to typed pipeline state.
// The Ref is shared across all items within a single Run; its value persists
// for the lifetime of the run. Runs at Concurrency(1).
//
//	var counterKey = kitsune.NewKey[int]("counter", 0)
//	numbered := kitsune.MapWith(p, counterKey,
//	    func(ctx context.Context, ref *kitsune.Ref[int], s string) (string, error) {
//	        n, _ := ref.UpdateAndGet(ctx, func(n int) (int, error) { return n + 1, nil })
//	        return fmt.Sprintf("%d: %s", n, s), nil
//	    },
//	)
func MapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I) (O, error), opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "map_with",
		name:        orDefault(cfg.name, "map_with"),
		concurrency: 1,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
			return newRef[S](initial, keyName, store, codec, ttl)
		})

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan((<-chan I)(inCh)) }()

			ref := rc.refs.get(keyName).(*Ref[S])
			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					out, err := fn(ctx, ref, item)
					if err != nil {
						return err
					}
					if err := outbox.Send(ctx, out); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// FlatMapWith applies a 1:N transform with access to typed pipeline state.
// The Ref is shared across all items within a single Run. Runs at Concurrency(1).
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "flat_map_with",
		name:        orDefault(cfg.name, "flat_map_with"),
		concurrency: 1,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
			return newRef[S](initial, keyName, store, codec, ttl)
		})

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan((<-chan I)(inCh)) }()

			ref := rc.refs.get(keyName).(*Ref[S])
			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					if err := fn(ctx, ref, item, func(out O) error {
						return outbox.Send(ctx, out)
					}); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// Keyed stateful operators (per-entity state)
// ---------------------------------------------------------------------------

// keyedRefMap holds a map from entity key → Ref. Initialized at run time.
type keyedRefMap[S any] struct {
	mu      sync.Mutex
	refs    map[string]*Ref[S]
	initial S
	store   internal.Store
	codec   internal.Codec
	keyName string
	ttl     time.Duration
}

func (m *keyedRefMap[S]) getRef(entityKey string) *Ref[S] {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ref, ok := m.refs[entityKey]; ok {
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
// share a Ref, while different keys get independent Refs. Runs at Concurrency(1).
//
//	var sessionKey = kitsune.NewKey[SessionState]("session", SessionState{})
//	result := kitsune.MapWithKey(events,
//	    func(e Event) string { return e.UserID },
//	    sessionKey,
//	    func(ctx context.Context, ref *kitsune.Ref[SessionState], e Event) (Result, error) {
//	        s, _ := ref.Get(ctx)
//	        s.EventCount++
//	        ref.Set(ctx, s)
//	        return Result{Count: s.EventCount}, nil
//	    },
//	)
func MapWithKey[I, O, S any](
	p *Pipeline[I],
	keyFn func(I) string,
	key Key[S],
	fn func(context.Context, *Ref[S], I) (O, error),
	opts ...StageOption,
) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "map_with_key",
		name:        orDefault(cfg.name, "map_with_key"),
		concurrency: 1,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
	}

	regName := key.name + ":__keyed__"
	initial := key.initial
	keyName := key.name
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		rc.refs.register(regName, func(store internal.Store, codec internal.Codec) any {
			return &keyedRefMap[S]{
				refs:    make(map[string]*Ref[S]),
				initial: initial,
				store:   store,
				codec:   codec,
				keyName: keyName,
				ttl:     ttl,
			}
		})

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan((<-chan I)(inCh)) }()

			km := rc.refs.get(regName).(*keyedRefMap[S])
			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					ref := km.getRef(keyFn(item))
					out, err := fn(ctx, ref, item)
					if err != nil {
						return err
					}
					if err := outbox.Send(ctx, out); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}

// FlatMapWithKey applies a 1:N transform with per-entity typed pipeline state.
// Runs at Concurrency(1).
func FlatMapWithKey[I, O, S any](
	p *Pipeline[I],
	keyFn func(I) string,
	key Key[S],
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	opts ...StageOption,
) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	meta := stageMeta{
		id:          id,
		kind:        "flat_map_with_key",
		name:        orDefault(cfg.name, "flat_map_with_key"),
		concurrency: 1,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
	}

	regName := key.name + ":__keyed__"
	initial := key.initial
	keyName := key.name
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		rc.refs.register(regName, func(store internal.Store, codec internal.Codec) any {
			return &keyedRefMap[S]{
				refs:    make(map[string]*Ref[S]),
				initial: initial,
				store:   store,
				codec:   codec,
				keyName: keyName,
				ttl:     ttl,
			}
		})

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		stage := func(ctx context.Context) error {
			defer close(ch)
			defer func() { go internal.DrainChan((<-chan I)(inCh)) }()

			km := rc.refs.get(regName).(*keyedRefMap[S])
			outbox := internal.NewBlockingOutbox(ch)

			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					ref := km.getRef(keyFn(item))
					if err := fn(ctx, ref, item, func(out O) error {
						return outbox.Send(ctx, out)
					}); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		rc.add(stage, m)
		return ch
	}
	return newPipeline(id, meta, build)
}
