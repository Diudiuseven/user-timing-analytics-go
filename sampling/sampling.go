// Package sampling implements deterministic, reproducible sampling.
//
// The keep decision for a stable key is hash(seed, key) < rate*2^64 with
// FNV-1a, so the same key under the same rate and seed is always accepted
// regardless of arrival order, retries or process restarts. Changing the
// seed rotates which entities are sampled without changing the machinery.
// Sampling happens at the SESSION level by default (the engine passes the
// session key) so that all data of one observed user-session shares one
// fate; this keeps per-session metrics intact instead of splicing entries.
package sampling

import (
	"encoding/binary"
	"hash/fnv"
)

// splitmix64 is Stafford's mix13 avalanche finalizer. Raw FNV-1a is biased
// over short, sequential keys, so hashes pass through this bijective mixer
// before the rate comparison; the result stays fully deterministic.
func splitmix64(z uint64) uint64 {
	z += 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Decider answers deterministic keep/drop decisions.
type Decider struct {
	rate    float64
	seed    uint64
	limit   uint64
	enabled bool
}

// NewDecider creates a Decider. rate<=0 drops everything, rate>=1 keeps
// everything (and skips hashing entirely).
func NewDecider(rate float64, seed uint64) *Decider {
	d := &Decider{rate: rate, seed: seed}
	if rate >= 1.0 {
		d.enabled = true // keep-all fast path
		return d
	}
	if rate <= 0 {
		return d
	}
	d.limit = uint64(rate * float64(^uint64(0)))
	return d
}

// Keep reports whether the key's entity is sampled in.
func (d *Decider) Keep(key string) bool {
	if d.rate >= 1.0 {
		return true
	}
	if d.rate <= 0 {
		return false
	}
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], d.seed)
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(key))
	return splitmix64(h.Sum64()) < d.limit
}

// Rate returns the configured sampling rate.
func (d *Decider) Rate() float64 { return d.rate }
