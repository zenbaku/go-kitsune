package kitsune

import "context"

const defaultLookupBatchSize = 100

// ---------------------------------------------------------------------------
// MapBatch
// ---------------------------------------------------------------------------

// MapBatch is a convenience wrapper around [Batch] followed by [FlatMap].
// It collects up to size items, passes the slice to fn, and flattens the
// returned slice back into individual items.
//
// Use [BatchTimeout] in opts to flush partial batches after a duration.
// Use [Concurrency] to process multiple batches in parallel.
//
//	enriched := kitsune.MapBatch(terms, 200,
//	    func(ctx context.Context, batch []EntityTerm) ([]EnrichedTerm, error) {
//	        ids := uniqueIDs(batch)
//	        entities, err := fetchEntities(ctx, ids)
//	        if err != nil { return nil, err }
//	        return buildEnriched(batch, entities), nil
//	    },
//	    kitsune.Concurrency(3),
//	)
func MapBatch[I, O any](p *Pipeline[I], size int, fn func(context.Context, []I) ([]O, error), opts ...StageOption) *Pipeline[O] {
	// opts are split: only BatchTimeout is forwarded to Batch (where it controls
	// partial-batch flushing); all opts go to FlatMap (where fn runs), so that
	// options like Concurrency, OnError, Buffer, and Ordered apply to fn execution
	// rather than to batch collection.
	batched := Batch(p, size, batchCollectOpts(opts)...)
	return FlatMap(batched, func(ctx context.Context, batch []I) ([]O, error) {
		return fn(ctx, batch)
	}, opts...)
}

// batchCollectOpts extracts the subset of StageOptions that are meaningful for
// the Batch collection stage: currently only BatchTimeout. All other options
// (Concurrency, OnError, Buffer, etc.) apply to the FlatMap processing stage.
// If new Batch-specific options are added in the future, include them here.
func batchCollectOpts(opts []StageOption) []StageOption {
	cfg := buildStageConfig(opts)
	if cfg.batchTimeout == 0 {
		return nil
	}
	return []StageOption{BatchTimeout(cfg.batchTimeout)}
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
type LookupConfig[T any, K comparable, V any] struct {
	Key       func(T) K
	Fetch     func(context.Context, []K) (map[K]V, error)
	BatchSize int
}

// LookupBy enriches each item with a value fetched in bulk, emitting a [Pair]
// of the original item and its looked-up value.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result carry
// the zero value for V in the pair.
//
// Combine with [ZipWith] when you need two independent lookups in parallel:
//
//	branches := kitsune.Broadcast(terms, 2)
//	withEntity := kitsune.LookupBy(branches[0], kitsune.LookupConfig[Term, int, Entity]{...})
//	withNames  := kitsune.LookupBy(branches[1], kitsune.LookupConfig[Term, int, []Name]{...})
//	enriched   := kitsune.ZipWith(withEntity, withNames,
//	    func(_ context.Context, e kitsune.Pair[Term, Entity], n kitsune.Pair[Term, []Name]) (EnrichedTerm, error) {
//	        return EnrichedTerm{Entity: e.Second, Names: n.Second}, nil
//	    },
//	)
func LookupBy[T any, K comparable, V any](p *Pipeline[T], cfg LookupConfig[T, K, V], opts ...StageOption) *Pipeline[Pair[T, V]] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
	}
	return MapBatch(p, size, func(ctx context.Context, batch []T) ([]Pair[T, V], error) {
		keys := uniqueKeys(batch, cfg.Key)
		m, err := cfg.Fetch(ctx, keys)
		if err != nil {
			return nil, err
		}
		result := make([]Pair[T, V], len(batch))
		for i, item := range batch {
			result[i] = Pair[T, V]{First: item, Second: m[cfg.Key(item)]}
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
//     to looked-up value. Items whose key is absent from the map receive the
//     zero value for V passed to Join.
//   - Join combines the original item with its fetched value into the output type.
//   - BatchSize controls how many items are collected before a single Fetch
//     call is made. Defaults to 100 when zero.
type EnrichConfig[T any, K comparable, V, O any] struct {
	Key       func(T) K
	Fetch     func(context.Context, []K) (map[K]V, error)
	Join      func(T, V) O
	BatchSize int
}

// Enrich enriches each item with a value fetched in bulk, combining the two
// with Join into a new output type.
//
// Items are batched internally; a single Fetch call is made per batch with
// deduplicated keys. Items whose key is absent from the Fetch result have
// Join called with the zero value for V.
//
//	enriched := kitsune.Enrich(terms, kitsune.EnrichConfig[Term, int, Entity, EnrichedTerm]{
//	    Key:   func(t Term) int { return t.EntityID },
//	    Fetch: func(ctx context.Context, ids []int) (map[int]Entity, error) {
//	        return fetchEntities(ctx, ids)
//	    },
//	    Join: func(t Term, e Entity) EnrichedTerm {
//	        return EnrichedTerm{Term: t, Entity: e}
//	    },
//	    BatchSize: 200,
//	}, kitsune.Concurrency(3))
func Enrich[T any, K comparable, V, O any](p *Pipeline[T], cfg EnrichConfig[T, K, V, O], opts ...StageOption) *Pipeline[O] {
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultLookupBatchSize
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
