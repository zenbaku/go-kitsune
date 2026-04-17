// Package kredis provides Redis-backed implementations of kitsune's
// [kitsune.Store], [kitsune.Cache], and [kitsune.DedupSet] interfaces,
// plus source and sink helpers for Redis lists.
//
// The caller owns the [redis.Client] lifecycle: create, configure, and close
// it yourself. Kitsune will never open or close connections.
//
// State backend (distributed key-value store for MapWithKey):
//
//	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
//	defer rdb.Close()
//	store := kredis.NewStore(rdb, "myapp:")
//	runner.Run(ctx, kitsune.WithStore(store))
//
// List pop source / push sink:
//
//	pipe := kredis.FromList(rdb, "myapp:queue")
//	pipe.ForEach(handle).Run(ctx)
//
//	sink := kredis.ListPush(rdb, "myapp:results")
//	pipe.ForEach(sink).Run(ctx)
//
// Delivery semantics: not applicable to Store, Cache, and DedupSet (state
// backends, not message brokers). FromList pops items from a Redis list
// (at-most-once; a popped item is not requeued on pipeline crash). ListPush
// appends synchronously (at-least-once when combined with retries).
package kredis

import (
	"context"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// Store: state backend
// ---------------------------------------------------------------------------

type redisStore struct {
	client *redis.Client
	prefix string
}

// NewStore returns a [kitsune.Store] backed by Redis.
// Keys are prefixed with prefix to avoid collisions.
func NewStore(client *redis.Client, prefix string) kitsune.Store {
	return &redisStore{client: client, prefix: prefix}
}

func (s *redisStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := s.client.Get(ctx, s.prefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (s *redisStore) Set(ctx context.Context, key string, value []byte) error {
	return s.client.Set(ctx, s.prefix+key, value, 0).Err()
}

func (s *redisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.prefix+key).Err()
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

type redisCache struct {
	client *redis.Client
	prefix string
}

// NewCache returns a [kitsune.Cache] backed by Redis.
// TTL is enforced by Redis natively via SET EX.
func NewCache(client *redis.Client, prefix string) kitsune.Cache {
	return &redisCache{client: client, prefix: prefix}
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, c.prefix+key, value, ttl).Err()
}

// ---------------------------------------------------------------------------
// DedupSet
// ---------------------------------------------------------------------------

type redisDedupSet struct {
	client *redis.Client
	key    string
}

// NewDedupSet returns a [kitsune.DedupSet] backed by a Redis set.
// All seen keys are stored as members of the given Redis key.
func NewDedupSet(client *redis.Client, key string) kitsune.DedupSet {
	return &redisDedupSet{client: client, key: key}
}

func (s *redisDedupSet) Contains(ctx context.Context, key string) (bool, error) {
	return s.client.SIsMember(ctx, s.key, key).Result()
}

func (s *redisDedupSet) Add(ctx context.Context, key string) error {
	return s.client.SAdd(ctx, s.key, key).Err()
}

// ---------------------------------------------------------------------------
// Source: read from a Redis list
// ---------------------------------------------------------------------------

// FromList creates a Pipeline that pops items from a Redis list (LPOP)
// until the list is empty.
func FromList(client *redis.Client, key string) *kitsune.Pipeline[string] {
	return kitsune.Generate(func(ctx context.Context, yield func(string) bool) error {
		for {
			val, err := client.LPop(ctx, key).Result()
			if err == redis.Nil {
				return nil // list empty
			}
			if err != nil {
				return err
			}
			if !yield(val) {
				return nil
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Sink: write to a Redis list
// ---------------------------------------------------------------------------

// ListPush returns a sink function that RPUSHes each item to a Redis list.
// Use with [kitsune.Pipeline.ForEach].
func ListPush(client *redis.Client, key string) func(context.Context, string) error {
	return func(ctx context.Context, val string) error {
		return client.RPush(ctx, key, val).Err()
	}
}
