package kitsune

import (
	"context"
	"sync"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
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

	// lastAccess is written only by keyedRefMap.getRef under keyedRefMap.mu.
	// It tracks the stage-level keyTTL (WithKeyTTL option), independent of
	// lastWrite (StateTTL, value-level). Not used by the public Ref API.
	lastAccess time.Time
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
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if v, found := ips.GetAny(r.key); found {
			// Safe: SetAny stores only T values for this key.
			return v.(T), nil
		}
		return r.initialVal, nil
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
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		ips.SetAny(r.key, value)
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
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		if v, found := ips.GetAny(r.key); found {
			// Safe: SetAny stores only T values for this key.
			return v.(T), nil
		}
		val, err := fn()
		if err != nil {
			var zero T
			return zero, err
		}
		ips.SetAny(r.key, val)
		return val, nil
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
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		var current T
		if v, found := ips.GetAny(r.key); found {
			// Safe: SetAny stores only T values for this key.
			current = v.(T)
		} else {
			current = r.initialVal
		}
		newVal, err := fn(current)
		if err != nil {
			var zero T
			return zero, err
		}
		ips.SetAny(r.key, newVal)
		return newVal, nil
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
	if ips, ok := r.store.(internal.InProcessStore); ok {
		r.mu.Lock()
		defer r.mu.Unlock()
		var current T
		if v, found := ips.GetAny(r.key); found {
			// Safe: SetAny stores only T values for this key.
			current = v.(T)
		} else {
			current = r.initialVal
		}
		newVal, err := fn(current)
		if err != nil {
			return err
		}
		ips.SetAny(r.key, newVal)
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
