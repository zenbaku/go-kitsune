package kitsune

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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
// Internal helpers
// ---------------------------------------------------------------------------

// fnv32str returns a stable FNV-1a hash of s. Used to shard items by key.
func fnv32str(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ---------------------------------------------------------------------------
// MapWith helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func mapWithSerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	ref *Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					start := time.Now()
					val, err, attempt := internal.ProcessItem(ctx, adaptedFn, item, cfg.errorHandler)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
					if err := outbox.Send(ctx, val); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// mapWithConcurrent launches n persistent worker goroutines that all read from
// the shared inCh. Each worker owns an independent Ref[S] initialised from
// key.initial, so no cross-worker locking is needed.
func mapWithConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			for _, ref := range workerRefs {
				ref := ref
				wg.Add(1)
				go func() {
					defer wg.Done()
					adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
					for {
						var item I
						var ok bool
						select {
						case item, ok = <-inCh:
							if !ok {
								return
							}
						case <-innerCtx.Done():
							return
						}
						start := time.Now()
						val, err, attempt := internal.ProcessItem(innerCtx, adaptedFn, item, cfg.errorHandler)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
						if sendErr := outbox.Send(innerCtx, val); sendErr != nil {
							reportErr(errCh, sendErr)
							cancel()
							return
						}
					}
				}()
			}

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// mapWithOrdered distributes items round-robin to n workers (each with its own
// Ref[S]) and re-assembles output in input order via per-item slot channels.
func mapWithOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		val O
		dur time.Duration
		err error
		att int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerRefs)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer: reads slots in insertion order, emits results in sequence.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					if err := outbox.Send(innerCtx, r.val); err != nil {
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- err
						return
					}
				}
				drainErrs <- nil
			}()

			// Workers: each reads from its dedicated channel.
			var workerWg sync.WaitGroup
			for i, ref := range workerRefs {
				ref := ref
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					adaptedFn := func(ctx context.Context, it I) (O, error) { return fn(ctx, ref, it) }
					for wi := range workerChs[i] {
						start := time.Now()
						val, err, att := internal.ProcessItem(innerCtx, adaptedFn, wi.item, cfg.errorHandler)
						dur := time.Since(start)
						wi.slot <- result{val: val, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher: round-robin items to workers with ordered slots.
			nextWorker := 0
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					select {
					case workerChs[nextWorker] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
					nextWorker = (nextWorker + 1) % n
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// MapWith — stateful 1:1 transform
// ---------------------------------------------------------------------------

// MapWith applies a 1:1 transform with access to typed pipeline state.
// The Ref is scoped to the run; its value persists for the lifetime of [Runner.Run].
// Each call to Run starts with a fresh Ref initialized to key.initial.
//
// With Concurrency(n) > 1, n worker goroutines are launched. Each worker owns
// an independent Ref[S] initialized from key.initial — state is worker-local,
// not shared across workers. Add Ordered() to preserve input order.
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart the stage on error or panic; the Ref value is
// preserved across restarts.
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
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "map_with",
		name:        orDefault(cfg.name, "map_with"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
			// Serial path: single shared Ref registered through the refRegistry.
			rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
				return newRef[S](initial, keyName, store, codec, ttl)
			})
			stage := func(ctx context.Context) error {
				ref := rc.refs.get(keyName).(*Ref[S])
				return mapWithSerial(inCh, ch, fn, ref, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			// Concurrent path: N worker-local Refs, one per worker.
			workersKey := keyName + ":__mw_workers__"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				refs := make([]*Ref[S], n)
				for i := range refs {
					refs[i] = newRef[S](initial, fmt.Sprintf("%s:worker:%d", keyName, i), store, codec, ttl)
				}
				return refs
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return mapWithOrdered(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return mapWithConcurrent(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// FlatMapWith helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func flatMapWithSerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	ref *Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }
		adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
			return fn(ctx, ref, it, yield)
		}

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					start := time.Now()
					err, attempt := internal.ProcessFlatMapItem(ctx, adaptedFn, item, cfg.errorHandler, send)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			for _, ref := range workerRefs {
				ref := ref
				wg.Add(1)
				go func() {
					defer wg.Done()
					adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
						return fn(ctx, ref, it, yield)
					}
					send := func(v O) error { return outbox.Send(innerCtx, v) }
					for {
						var item I
						var ok bool
						select {
						case item, ok = <-inCh:
							if !ok {
								return
							}
						case <-innerCtx.Done():
							return
						}
						start := time.Now()
						err, attempt := internal.ProcessFlatMapItem(innerCtx, adaptedFn, item, cfg.errorHandler, send)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
					}
				}()
			}

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerRefs []*Ref[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		items []O
		dur   time.Duration
		err   error
		att   int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerRefs)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer: emit each item group in order.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					for _, v := range r.items {
						if err := outbox.Send(innerCtx, v); err != nil {
							cancel()
							go func() {
								for s := range slots {
									<-s
								}
							}()
							drainErrs <- err
							return
						}
					}
				}
				drainErrs <- nil
			}()

			// Workers.
			var workerWg sync.WaitGroup
			for i, ref := range workerRefs {
				ref := ref
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					for wi := range workerChs[i] {
						var items []O
						collectYield := func(v O) error {
							items = append(items, v)
							return nil
						}
						adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
							return fn(ctx, ref, it, yield)
						}
						start := time.Now()
						err, att := internal.ProcessFlatMapItem(innerCtx, adaptedFn, wi.item, cfg.errorHandler, collectYield)
						dur := time.Since(start)
						wi.slot <- result{items: items, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher: round-robin with ordered slots.
			nextWorker := 0
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					select {
					case workerChs[nextWorker] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
					nextWorker = (nextWorker + 1) % n
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// FlatMapWith — stateful 1:N transform
// ---------------------------------------------------------------------------

// FlatMapWith applies a 1:N transform with access to typed pipeline state.
// The Ref is scoped to the run; its value persists for the lifetime of [Runner.Run].
//
// With Concurrency(n) > 1, n worker goroutines are launched. Each worker owns
// an independent Ref[S] initialized from key.initial. Add Ordered() to preserve
// input order (all outputs for item i are emitted before any outputs for item i+1).
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart the stage on error or panic; the Ref value is
// preserved across restarts.
func FlatMapWith[I, O, S any](p *Pipeline[I], key Key[S], fn func(context.Context, *Ref[S], I, func(O) error) error, opts ...StageOption) *Pipeline[O] {
	track(p)
	cfg := buildStageConfig(opts)
	id := nextPipelineID()
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "flat_map_with",
		name:        orDefault(cfg.name, "flat_map_with"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	keyName := key.name
	initial := key.initial
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
			rc.refs.register(keyName, func(store internal.Store, codec internal.Codec) any {
				return newRef[S](initial, keyName, store, codec, ttl)
			})
			stage := func(ctx context.Context) error {
				ref := rc.refs.get(keyName).(*Ref[S])
				return flatMapWithSerial(inCh, ch, fn, ref, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			workersKey := keyName + ":__fmw_workers__"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				refs := make([]*Ref[S], n)
				for i := range refs {
					refs[i] = newRef[S](initial, fmt.Sprintf("%s:fmw:worker:%d", keyName, i), store, codec, ttl)
				}
				return refs
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return flatMapWithOrdered(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerRefs := rc.refs.get(workersKey).([]*Ref[S])
					return flatMapWithConcurrent(inCh, ch, fn, workerRefs, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
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

// ---------------------------------------------------------------------------
// MapWithKey helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func mapWithKeySerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I) (O, error),
	km *keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					ref := km.getRef(keyFn(item))
					start := time.Now()
					val, err, attempt := internal.ProcessItem(ctx, func(ctx context.Context, it I) (O, error) {
						return fn(ctx, ref, it)
					}, item, cfg.errorHandler)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
					if err := outbox.Send(ctx, val); err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// mapWithKeyConcurrent routes items to n persistent workers by hash(key)%n.
// Each worker owns a disjoint partition of the key space with its own local
// keyedRefMap — no cross-worker locking on entity state.
func mapWithKeyConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerMaps []*keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerMaps)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan I, n)
			for i := range workerChs {
				workerChs[i] = make(chan I, cfg.buffer+1)
			}
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			// Workers: each owns a partition of the key space.
			for i, km := range workerMaps {
				km := km
				wg.Add(1)
				go func() {
					defer wg.Done()
					for item := range workerChs[i] {
						ref := km.getRef(keyFn(item))
						start := time.Now()
						val, err, attempt := internal.ProcessItem(innerCtx, func(ctx context.Context, it I) (O, error) {
							return fn(ctx, ref, it)
						}, item, cfg.errorHandler)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
						if sendErr := outbox.Send(innerCtx, val); sendErr != nil {
							reportErr(errCh, sendErr)
							cancel()
							return
						}
					}
				}()
			}

			// Dispatcher: routes items to workers by key hash.
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					shard := int(fnv32str(keyFn(item))) % n
					select {
					case workerChs[shard] <- item:
					case <-innerCtx.Done():
						return
					}
				}
			}()

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// mapWithKeyOrdered routes items by hash(key)%n to persistent workers and
// re-assembles output in input order via per-item slot channels.
func mapWithKeyOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I) (O, error),
	workerMaps []*keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		val O
		dur time.Duration
		err error
		att int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerMaps)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer: reads slots in insertion order.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					if err := outbox.Send(innerCtx, r.val); err != nil {
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- err
						return
					}
				}
				drainErrs <- nil
			}()

			// Workers: each owns a key-space partition.
			var workerWg sync.WaitGroup
			for i, km := range workerMaps {
				km := km
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					for wi := range workerChs[i] {
						ref := km.getRef(keyFn(wi.item))
						start := time.Now()
						val, err, att := internal.ProcessItem(innerCtx, func(ctx context.Context, it I) (O, error) {
							return fn(ctx, ref, it)
						}, wi.item, cfg.errorHandler)
						dur := time.Since(start)
						wi.slot <- result{val: val, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher: routes by key hash, inserts slots in input order.
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					shard := int(fnv32str(keyFn(item))) % n
					select {
					case workerChs[shard] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// MapWithKey — per-entity stateful 1:1 transform
// ---------------------------------------------------------------------------

// MapWithKey applies a 1:1 transform with per-entity typed pipeline state.
// keyFn extracts a string entity key from each item; items with the same key
// share a Ref, while different keys get independent Refs.
//
// With Concurrency(n) > 1, the key space is sharded across n persistent worker
// goroutines using a stable hash of the entity key. Each worker owns a disjoint
// partition of the key space and its own local state map — completely lock-free
// per-worker processing with no cross-worker coordination. Same-key items are
// always routed to the same worker, preserving per-entity state semantics.
// Add Ordered() to emit results in input order.
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart on error or panic; per-entity Refs are preserved
// across restarts.
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
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "map_with_key",
		name:        orDefault(cfg.name, "map_with_key"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	regName := key.name + ":__keyed__"
	initial := key.initial
	keyName := key.name
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
			// Serial path: single shared keyedRefMap.
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
			stage := func(ctx context.Context) error {
				km := rc.refs.get(regName).(*keyedRefMap[S])
				return mapWithKeySerial(inCh, ch, keyFn, fn, km, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			// Concurrent path: N worker-local keyedRefMaps (no shared state).
			workersKey := regName + ":workers"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				maps := make([]*keyedRefMap[S], n)
				for i := range maps {
					maps[i] = &keyedRefMap[S]{
						refs:    make(map[string]*Ref[S]),
						initial: initial,
						store:   store,
						codec:   codec,
						keyName: keyName,
						ttl:     ttl,
					}
				}
				return maps
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerMaps := rc.refs.get(workersKey).([]*keyedRefMap[S])
					return mapWithKeyOrdered(inCh, ch, keyFn, fn, workerMaps, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerMaps := rc.refs.get(workersKey).([]*keyedRefMap[S])
					return mapWithKeyConcurrent(inCh, ch, keyFn, fn, workerMaps, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
		return ch
	}
	return newPipeline(id, meta, build)
}

// ---------------------------------------------------------------------------
// FlatMapWithKey helpers (serial / concurrent / ordered)
// ---------------------------------------------------------------------------

func flatMapWithKeySerial[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	km *keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var processed, errs int64
		defer func() { hook.OnStageDone(ctx, cfg.name, processed, errs) }()

		outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
		send := func(v O) error { return outbox.Send(ctx, v) }

		inner := func() error {
			for {
				select {
				case item, ok := <-inCh:
					if !ok {
						return nil
					}
					ref := km.getRef(keyFn(item))
					adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
						return fn(ctx, ref, it, yield)
					}
					start := time.Now()
					err, attempt := internal.ProcessFlatMapItem(ctx, adaptedFn, item, cfg.errorHandler, send)
					dur := time.Since(start)
					if err == internal.ErrSkipped {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						continue
					}
					if err != nil {
						errs++
						hook.OnItem(ctx, cfg.name, dur, err)
						return internal.WrapStageErr(cfg.name, err, attempt)
					}
					processed++
					hook.OnItem(ctx, cfg.name, dur, nil)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithKeyConcurrent[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerMaps []*keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerMaps)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan I, n)
			for i := range workerChs {
				workerChs[i] = make(chan I, cfg.buffer+1)
			}
			errCh := make(chan error, 1)
			var wg sync.WaitGroup

			// Workers.
			for i, km := range workerMaps {
				km := km
				wg.Add(1)
				go func() {
					defer wg.Done()
					send := func(v O) error { return outbox.Send(innerCtx, v) }
					for item := range workerChs[i] {
						ref := km.getRef(keyFn(item))
						adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
							return fn(ctx, ref, it, yield)
						}
						start := time.Now()
						err, attempt := internal.ProcessFlatMapItem(innerCtx, adaptedFn, item, cfg.errorHandler, send)
						dur := time.Since(start)
						if err == internal.ErrSkipped {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							continue
						}
						if err != nil {
							errCount.Add(1)
							hook.OnItem(ctx, cfg.name, dur, err)
							reportErr(errCh, internal.WrapStageErr(cfg.name, err, attempt))
							cancel()
							return
						}
						procCount.Add(1)
						hook.OnItem(ctx, cfg.name, dur, nil)
					}
				}()
			}

			// Dispatcher.
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					shard := int(fnv32str(keyFn(item))) % n
					select {
					case workerChs[shard] <- item:
					case <-innerCtx.Done():
						return
					}
				}
			}()

			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return nil
			}
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

func flatMapWithKeyOrdered[I, O, S any](
	inCh <-chan I,
	outCh chan O,
	keyFn func(I) string,
	fn func(context.Context, *Ref[S], I, func(O) error) error,
	workerMaps []*keyedRefMap[S],
	cfg stageConfig,
	hook internal.Hook,
) stageFunc {
	type result struct {
		items []O
		dur   time.Duration
		err   error
		att   int
	}
	type workItem struct {
		item I
		slot chan result
	}
	return func(ctx context.Context) error {
		defer close(outCh)
		defer func() { go internal.DrainChan(inCh) }()

		hook.OnStageStart(ctx, cfg.name)
		var procCount, errCount atomic.Int64
		defer func() { hook.OnStageDone(ctx, cfg.name, procCount.Load(), errCount.Load()) }()

		inner := func() error {
			n := len(workerMaps)
			innerCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			outbox := internal.NewOutbox(outCh, cfg.overflow, hook, cfg.name)
			workerChs := make([]chan workItem, n)
			for i := range workerChs {
				workerChs[i] = make(chan workItem, cfg.buffer+1)
			}
			slots := make(chan chan result, n*2)
			drainErrs := make(chan error, 1)

			// Drainer.
			go func() {
				for slotCh := range slots {
					r := <-slotCh
					if r.err == internal.ErrSkipped {
						continue
					}
					if r.err != nil {
						errCount.Add(1)
						hook.OnItem(ctx, cfg.name, r.dur, r.err)
						cancel()
						go func() {
							for s := range slots {
								<-s
							}
						}()
						drainErrs <- internal.WrapStageErr(cfg.name, r.err, r.att)
						return
					}
					procCount.Add(1)
					hook.OnItem(ctx, cfg.name, r.dur, nil)
					for _, v := range r.items {
						if err := outbox.Send(innerCtx, v); err != nil {
							cancel()
							go func() {
								for s := range slots {
									<-s
								}
							}()
							drainErrs <- err
							return
						}
					}
				}
				drainErrs <- nil
			}()

			// Workers.
			var workerWg sync.WaitGroup
			for i, km := range workerMaps {
				km := km
				workerWg.Add(1)
				go func() {
					defer workerWg.Done()
					for wi := range workerChs[i] {
						ref := km.getRef(keyFn(wi.item))
						var items []O
						collectYield := func(v O) error {
							items = append(items, v)
							return nil
						}
						adaptedFn := func(ctx context.Context, it I, yield func(O) error) error {
							return fn(ctx, ref, it, yield)
						}
						start := time.Now()
						err, att := internal.ProcessFlatMapItem(innerCtx, adaptedFn, wi.item, cfg.errorHandler, collectYield)
						dur := time.Since(start)
						wi.slot <- result{items: items, dur: dur, err: err, att: att}
					}
				}()
			}

			// Dispatcher.
			func() {
				defer func() {
					for _, wCh := range workerChs {
						close(wCh)
					}
				}()
				for {
					var item I
					var ok bool
					select {
					case item, ok = <-inCh:
						if !ok {
							return
						}
					case <-innerCtx.Done():
						return
					}
					slotCh := make(chan result, 1)
					select {
					case slots <- slotCh:
					case <-innerCtx.Done():
						return
					}
					shard := int(fnv32str(keyFn(item))) % n
					select {
					case workerChs[shard] <- workItem{item: item, slot: slotCh}:
					case <-innerCtx.Done():
						return
					}
				}
			}()

			workerWg.Wait()
			close(slots)
			return <-drainErrs
		}
		return internal.Supervise(ctx, cfg.supervision, hook, cfg.name, inner)
	}
}

// ---------------------------------------------------------------------------
// FlatMapWithKey — per-entity stateful 1:N transform
// ---------------------------------------------------------------------------

// FlatMapWithKey applies a 1:N transform with per-entity typed pipeline state.
// keyFn extracts a string entity key from each item; items with the same key
// share a Ref, while different keys get independent Refs.
//
// With Concurrency(n) > 1, the key space is sharded across n persistent workers
// by stable hash of the entity key — same-key items always reach the same worker,
// preserving per-entity state without any cross-worker locking.
// Add Ordered() to emit all outputs for item i before any outputs for item i+1.
//
// Use OnError to control what happens when fn returns an error (default: Halt).
// Use Supervise to restart on error or panic; per-entity Refs are preserved
// across restarts.
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
	n := max(1, cfg.concurrency)
	meta := stageMeta{
		id:          id,
		kind:        "flat_map_with_key",
		name:        orDefault(cfg.name, "flat_map_with_key"),
		concurrency: n,
		buffer:      cfg.buffer,
		overflow:    cfg.overflow,
		inputs:      []int{p.id},
		hasSuperv:   cfg.supervision.HasSupervision(),
	}

	regName := key.name + ":__keyed__"
	initial := key.initial
	keyName := key.name
	ttl := key.ttl

	build := func(rc *runCtx) chan O {
		if existing := rc.getChan(id); existing != nil {
			return existing.(chan O)
		}

		inCh := p.build(rc)
		ch := make(chan O, cfg.buffer)
		m := meta
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.errorHandler = resolveHandler(cfg, rc)

		if n == 1 {
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
			stage := func(ctx context.Context) error {
				km := rc.refs.get(regName).(*keyedRefMap[S])
				return flatMapWithKeySerial(inCh, ch, keyFn, fn, km, cfg, hook)(ctx)
			}
			rc.add(stage, m)
		} else {
			workersKey := regName + ":fmwk:workers"
			rc.refs.register(workersKey, func(store internal.Store, codec internal.Codec) any {
				maps := make([]*keyedRefMap[S], n)
				for i := range maps {
					maps[i] = &keyedRefMap[S]{
						refs:    make(map[string]*Ref[S]),
						initial: initial,
						store:   store,
						codec:   codec,
						keyName: keyName,
						ttl:     ttl,
					}
				}
				return maps
			})
			var stage stageFunc
			if cfg.ordered {
				stage = func(ctx context.Context) error {
					workerMaps := rc.refs.get(workersKey).([]*keyedRefMap[S])
					return flatMapWithKeyOrdered(inCh, ch, keyFn, fn, workerMaps, cfg, hook)(ctx)
				}
			} else {
				stage = func(ctx context.Context) error {
					workerMaps := rc.refs.get(workersKey).([]*keyedRefMap[S])
					return flatMapWithKeyConcurrent(inCh, ch, keyFn, fn, workerMaps, cfg, hook)(ctx)
				}
			}
			rc.add(stage, m)
		}
		return ch
	}
	return newPipeline(id, meta, build)
}
