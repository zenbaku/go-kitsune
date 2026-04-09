package internal

import (
	"context"
	"fmt"
	"hash/maphash"
	"math"
	"sync"
)

// ---------------------------------------------------------------------------
// BloomDedupSet
// ---------------------------------------------------------------------------

// BloomDedupSet returns an in-process probabilistic deduplication set backed
// by a Bloom filter. Memory usage is bounded regardless of key-space size, at
// the cost of a configurable false-positive rate: items may occasionally be
// reported as seen when they have not been. Inserted items are never missed
// (zero false-negative rate).
//
// expectedItems is the anticipated number of unique keys.
// falsePositiveRate is the desired probability of a false positive (e.g. 0.01
// for 1%). Panics if expectedItems <= 0 or falsePositiveRate is not in (0,1).
func BloomDedupSet(expectedItems int, falsePositiveRate float64) DedupSet {
	if expectedItems <= 0 {
		panic(fmt.Sprintf("kitsune: BloomDedupSet: expectedItems must be > 0, got %d", expectedItems))
	}
	if falsePositiveRate <= 0 || falsePositiveRate >= 1 {
		panic(fmt.Sprintf("kitsune: BloomDedupSet: falsePositiveRate must be in (0,1), got %v", falsePositiveRate))
	}
	m, k := optimalBloomParams(expectedItems, falsePositiveRate)
	words := (m + 63) / 64
	return &bloomDedupSet{
		bits:  make([]uint64, words),
		m:     m,
		k:     k,
		seed1: maphash.MakeSeed(),
		seed2: maphash.MakeSeed(),
	}
}

type bloomDedupSet struct {
	mu    sync.RWMutex
	bits  []uint64
	m     uint64
	k     uint
	seed1 maphash.Seed
	seed2 maphash.Seed
}

func (b *bloomDedupSet) Contains(_ context.Context, key string) (bool, error) {
	h1, h2 := bloomHash(key, b.seed1, b.seed2)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for i := uint(0); i < b.k; i++ {
		pos := (h1 + uint64(i)*h2) % b.m
		if b.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false, nil
		}
	}
	return true, nil
}

func (b *bloomDedupSet) Add(_ context.Context, key string) error {
	h1, h2 := bloomHash(key, b.seed1, b.seed2)
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := uint(0); i < b.k; i++ {
		pos := (h1 + uint64(i)*h2) % b.m
		b.bits[pos/64] |= 1 << (pos % 64)
	}
	return nil
}

// bloomHash returns two independent 64-bit hashes for key using the
// Kirsch-Mitzenmacker approach. The caller derives k hash positions as
// h1 + i*h2 (mod m).
func bloomHash(key string, seed1, seed2 maphash.Seed) (uint64, uint64) {
	var h1, h2 maphash.Hash
	h1.SetSeed(seed1)
	_, _ = h1.WriteString(key)
	v1 := h1.Sum64()

	h2.SetSeed(seed2)
	_, _ = h2.WriteString(key)
	v2 := h2.Sum64()

	// Ensure h2 is odd to guarantee full coverage of the bit array.
	v2 |= 1
	return v1, v2
}

// optimalBloomParams calculates the optimal bit array size m and number of
// hash functions k for the given expected element count n and false-positive
// rate p.
func optimalBloomParams(n int, p float64) (m uint64, k uint) {
	// m = ceil(-n * ln(p) / (ln(2))^2)
	ln2 := math.Log(2)
	mf := -float64(n) * math.Log(p) / (ln2 * ln2)
	m = uint64(math.Ceil(mf))
	if m == 0 {
		m = 1
	}
	// k = round(m/n * ln(2))
	kf := float64(m) / float64(n) * ln2
	ki := uint(math.Round(kf))
	if ki < 1 {
		ki = 1
	}
	return m, ki
}
