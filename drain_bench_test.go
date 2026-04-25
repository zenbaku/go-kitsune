package kitsune_test

// BenchmarkDrainBurst measures the teardown performance of a multi-stage
// pipeline under the cooperative drain protocol.
//
// The pipeline is: Repeatedly -> Map*10 -> Take(1) -> ForEach
//
// This is the exact scenario that caused goroutine burst on mass teardown
// before cooperative drain was introduced. Each b.N iteration runs one full
// pipeline lifecycle, including goroutine launch and cleanup.
//
// Run with:
//
//	go test -bench=BenchmarkDrainBurst -benchmem -benchtime=3s -count=3 .

import (
	"context"
	"testing"

	kitsune "github.com/zenbaku/go-kitsune"
)

func BenchmarkDrainBurst(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p := kitsune.Repeatedly(func() int {
			return 1
		})
		for j := 0; j < 10; j++ {
			p = kitsune.Map(p, func(ctx context.Context, v int) (int, error) {
				return v + 1, nil
			})
		}
		taken := kitsune.Take(p, 1)
		var got int
		_, _ = taken.ForEach(func(ctx context.Context, v int) error {
			got = v
			return nil
		}).Run(ctx)
		_ = got
	}
}
