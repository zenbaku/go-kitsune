package kitsune

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenbaku/go-kitsune/internal"
)

// ---------------------------------------------------------------------------
// Keyed stateful operators (per-entity state)
// ---------------------------------------------------------------------------

// keyedRefMap holds a map from entity key → Ref. Initialized at run time.
type keyedRefMap[S any] struct {
	mu        sync.Mutex
	refs      map[string]*Ref[S]
	initial   S
	store     internal.Store
	codec     internal.Codec
	keyName   string
	ttl       time.Duration    // StateTTL: value-level TTL (from Key[S])
	keyTTL    time.Duration    // WithKeyTTL: map-entry eviction TTL (from StageOption)
	nextSweep time.Time        // when to next run an opportunistic sweep of stale entries
	now       func() time.Time // injectable clock for testing; nil means time.Now
}

func (m *keyedRefMap[S]) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *keyedRefMap[S]) getRef(entityKey string) *Ref[S] {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()

	// Opportunistic sweep: scan for other stale entries at most once every
	// keyTTL/8 to amortise cost across item arrivals. Skips the requested
	// key (handled below) to avoid double-deletion.
	if m.keyTTL > 0 && !now.Before(m.nextSweep) {
		cutoff := now.Add(-m.keyTTL)
		for k, r := range m.refs {
			if k != entityKey && r.lastAccess.Before(cutoff) {
				delete(m.refs, k)
			}
		}
		m.nextSweep = now.Add(m.keyTTL / 8)
	}

	if ref, ok := m.refs[entityKey]; ok {
		evict := false
		// Stage-level keyTTL: evict the entry if the key has been inactive.
		if m.keyTTL > 0 && !ref.lastAccess.IsZero() && now.Sub(ref.lastAccess) > m.keyTTL {
			evict = true
		}
		// Value-level StateTTL (existing behaviour): evict when the stored
		// value has expired so the next access starts with the initial value.
		if !evict && m.ttl > 0 && !ref.lastWrite.IsZero() && now.After(ref.lastWrite.Add(m.ttl)) {
			evict = true
		}
		if evict {
			delete(m.refs, entityKey)
		} else {
			ref.lastAccess = now
			return ref
		}
	}

	storeKey := m.keyName + ":" + entityKey
	ref := newRef[S](m.initial, storeKey, m.store, m.codec, m.ttl)
	ref.lastAccess = now
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
		inputs:      []int64{p.id},
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
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.buffer = buf
		cfg.errorHandler = resolveHandler(cfg, rc)
		keyTTL := rc.effectiveKeyTTL(cfg)

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
					keyTTL:  keyTTL,
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
						keyTTL:  keyTTL,
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
		inputs:      []int64{p.id},
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
		buf := rc.effectiveBufSize(cfg)
		ch := make(chan O, buf)
		m := meta
		m.buffer = buf
		m.getChanLen = func() int { return len(ch) }
		m.getChanCap = func() int { return cap(ch) }
		rc.setChan(id, ch)

		hook := rc.hook
		if hook == nil {
			hook = internal.NoopHook{}
		}
		cfg := cfg // local copy; resolve pipeline-level default handler
		cfg.buffer = buf
		cfg.errorHandler = resolveHandler(cfg, rc)
		keyTTL := rc.effectiveKeyTTL(cfg)

		if n == 1 {
			rc.refs.register(regName, func(store internal.Store, codec internal.Codec) any {
				return &keyedRefMap[S]{
					refs:    make(map[string]*Ref[S]),
					initial: initial,
					store:   store,
					codec:   codec,
					keyName: keyName,
					ttl:     ttl,
					keyTTL:  keyTTL,
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
						keyTTL:  keyTTL,
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
