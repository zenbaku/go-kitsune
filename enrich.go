package kitsune

import (
	"context"
	"time"
)

const defaultLookupBatchSize = 100

// Enriched is the output of [LookupBy]: an item paired with the value fetched
// for its key. Items whose key is absent from the [LookupConfig.Fetch] result
// carry the zero value for V.
type Enriched[T any, V any] struct {
	Item  T
	Value V
}

// ---------------------------------------------------------------------------
// LookupBy
// ---------------------------------------------------------------------------

// LookupConfig configures a [LookupBy] stage.
//
//   - Key extracts the lookup key from each item.
//   - Fetch receives a deduplicated slice of keys and returns a map from key
//     to looked-up value. Items whose key is absent from the map receive the
//     zero value for V.
//   - BatchSize controls how many items are collected before a single Fetch
//     call is made. Defaults to 100 when zero.
//   - BatchTimeout, when non-zero, flushes a partial batch after the duration
//     elapses with no new item. Without it, items sit in the internal buffer
//     until BatchSize is reached or the source closes, which can introduce
//     unbounded latency under low throughput.
type LookupConfig[T any, K comparable, V any] struct {
	Key          func(T) K
	Fetch        func(context.Context, []K) (map[K]V, error)
	BatchSize    int
	BatchTimeout time.Duration
}

// NewLookupConfig creates a LookupConfig with the given key extractor and fetch
// function. BatchSize defaults to 100.
func NewLookupConfig[T any, K comparable, V any](
	key func(T) K,
	fetch func(context.Context, []K) (map[K]V, error),
) LookupConfig[T, K, V] {
	return LookupConfig[T, K, V]{
		Key:       key,
		Fetch:     fetch,
		BatchSize: defaultLookupBatchSize,
	}
}

// LookupBy enriches each item with a value fetched in bulk, emitting an
// [Enriched] value carrying the original item and its looked-up value.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result carry
// the zero value for V.
//
//	cfg := kitsune.NewLookupConfig(
//	    func(id int) int { return id },
//	    func(ctx context.Context, ids []int) (map[int]User, error) {
//	        return fetchUsers(ctx, ids)
//	    },
//	)
//	withUsers := kitsune.LookupBy(eventIDs, cfg)
func LookupBy[T any, K comparable, V any](p *Pipeline[T], cfg LookupConfig[T, K, V], opts ...StageOption) *Pipeline[Enriched[T, V]] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]Enriched[T, V], error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]Enriched[T, V], len(batch))
		for i, item := range batch {
			result[i] = Enriched[T, V]{Item: item, Value: m[cfg.Key(item)]}
		}
		return result, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// Enrich
// ---------------------------------------------------------------------------

// EnrichConfig configures an [Enrich] stage.
//
//   - Key extracts the lookup key from each item.
//   - Fetch receives a deduplicated slice of keys and returns a map from key
//     to looked-up value.
//   - Join combines the original item with its fetched value into the output type.
//   - BatchSize controls how many items are collected before a single Fetch
//     call is made. Defaults to 100 when zero.
//   - BatchTimeout, when non-zero, flushes a partial batch after the duration
//     elapses with no new item. Without it, items sit in the internal buffer
//     until BatchSize is reached or the source closes, which can introduce
//     unbounded latency under low throughput.
type EnrichConfig[T any, K comparable, V, O any] struct {
	Key          func(T) K
	Fetch        func(context.Context, []K) (map[K]V, error)
	Join         func(T, V) O
	BatchSize    int
	BatchTimeout time.Duration
}

// NewEnrichConfig creates an EnrichConfig with the given key extractor, fetch
// function, and join function. BatchSize defaults to 100.
func NewEnrichConfig[T any, K comparable, V, O any](
	key func(T) K,
	fetch func(context.Context, []K) (map[K]V, error),
	join func(T, V) O,
) EnrichConfig[T, K, V, O] {
	return EnrichConfig[T, K, V, O]{
		Key:       key,
		Fetch:     fetch,
		Join:      join,
		BatchSize: defaultLookupBatchSize,
	}
}

// Enrich enriches each item with a value fetched in bulk, combining the two
// with Join into a new output type.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result have
// Join called with the zero value for V.
//
//	cfg := kitsune.NewEnrichConfig(
//	    func(e Event) int { return e.UserID },
//	    func(ctx context.Context, ids []int) (map[int]User, error) {
//	        return fetchUsers(ctx, ids)
//	    },
//	    func(e Event, u User) EnrichedEvent {
//	        return EnrichedEvent{Event: e, User: u}
//	    },
//	)
//	enriched := kitsune.Enrich(events, cfg, kitsune.Concurrency(3))
func Enrich[T any, K comparable, V, O any](p *Pipeline[T], cfg EnrichConfig[T, K, V, O], opts ...StageOption) *Pipeline[O] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	if cfg.BatchTimeout > 0 {
		opts = append([]StageOption{BatchTimeout(cfg.BatchTimeout)}, opts...)
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]O, error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]O, len(batch))
		for i, item := range batch {
			result[i] = cfg.Join(item, m[cfg.Key(item)])
		}
		return result, nil
	}, opts...)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// uniqueKeys extracts a deduplicated, order-preserving slice of keys from a batch.
func uniqueKeys[T any, K comparable](batch []T, keyFn func(T) K) []K {
	seen := make(map[K]struct{}, len(batch))
	keys := make([]K, 0, len(batch))
	for _, item := range batch {
		k := keyFn(item)
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	return keys
}
