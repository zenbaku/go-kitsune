package kitsune

import (
	"context"

	"github.com/zenbaku/go-kitsune/engine"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// RateLimit — token-bucket rate limiting
// ---------------------------------------------------------------------------

// RateLimitMode controls how the rate limiter behaves when the bucket is empty.
type RateLimitMode int

const (
	// RateLimitWait blocks the pipeline until a token is available, creating
	// natural backpressure. This is the default.
	RateLimitWait RateLimitMode = iota

	// RateLimitDrop silently discards items when no token is available,
	// allowing upstream stages to run at their natural speed.
	RateLimitDrop
)

type rateLimitConfig struct {
	burst int
	mode  RateLimitMode
}

func defaultRLConfig() *rateLimitConfig {
	return &rateLimitConfig{burst: 1}
}

// Burst sets the token-bucket burst size — the maximum number of items that
// may pass instantaneously when the bucket is full. Defaults to 1.
func Burst(n int) StageOption {
	return func(c *stageConfig) {
		if c.rlConfig == nil {
			c.rlConfig = defaultRLConfig()
		}
		c.rlConfig.burst = n
	}
}

// RateMode sets the behavior when no token is available.
// Defaults to [RateLimitWait].
func RateMode(m RateLimitMode) StageOption {
	return func(c *stageConfig) {
		if c.rlConfig == nil {
			c.rlConfig = defaultRLConfig()
		}
		c.rlConfig.mode = m
	}
}

// RateLimit enforces a token-bucket rate limit on a pipeline.
// itemsPerSecond sets the steady-state throughput; tokens accumulate up to the
// configured burst (default: 1).
//
// In [RateLimitWait] mode (default) the stage blocks until a token is
// available, propagating backpressure to upstream stages. In [RateLimitDrop]
// mode items are silently discarded when the bucket is empty, which is
// equivalent to a non-blocking throttle with configurable burst.
//
// Unlike [Throttle] — which allows exactly one item per fixed window —
// RateLimit supports sustained N items/sec with burst and proper
// backpressure.
//
//	// Allow 100 items/sec with a burst of 10, blocking when rate is exceeded.
//	limited := kitsune.RateLimit(p, 100, kitsune.Burst(10))
//
//	// Allow 50 items/sec, drop excess silently.
//	limited := kitsune.RateLimit(p, 50, kitsune.Burst(5), kitsune.RateMode(kitsune.RateLimitDrop))
func RateLimit[T any](p *Pipeline[T], itemsPerSecond float64, opts ...StageOption) *Pipeline[T] {
	sc := buildStageConfig(opts)
	rlCfg := sc.rlConfig
	if rlCfg == nil {
		rlCfg = defaultRLConfig()
	}
	burst := rlCfg.burst
	if burst < 1 {
		burst = 1
	}
	limiter := rate.NewLimiter(rate.Limit(itemsPerSecond), burst)

	if rlCfg.mode == RateLimitDrop {
		return Map(p, func(_ context.Context, item T) (T, error) {
			if !limiter.Allow() {
				var zero T
				return zero, engine.ErrSkipped
			}
			return item, nil
		}, append(opts, Concurrency(1))...)
	}

	// Wait mode — block until a token is available.
	return Map(p, func(ctx context.Context, item T) (T, error) {
		if err := limiter.Wait(ctx); err != nil {
			var zero T
			return zero, err
		}
		return item, nil
	}, append(opts, Concurrency(1))...)
}
