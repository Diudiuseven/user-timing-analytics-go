// Package timesync estimates client->server clock offset from batch
// arrival samples and normalizes client timestamps onto a monotonic
// server-axis timeline.
//
// Design notes:
//
//   - Offset is the MEDIAN of (serverReceiveMs - clientSendMs) samples of a
//     session. The median is robust against a minority of delayed batches,
//     unlike the minimum, while remaining a deterministic order-independent
//     statistic: feeding the same samples in any order yields the same
//     offset, which is essential because batches may arrive out of order or
//     be replayed after a restart.
//   - Normalization converts client wall-clock ms to server-axis ms and then
//     enforces monotonicity: a timestamp that would move the timeline
//     backwards (clock rollback / NTP step) is clamped to the largest
//     timestamp already observed. Timestamps that jump forwards or backwards
//     by more than MaxClockJumpMs are additionally flagged as "jumped";
//     forward jumps are accepted as-is (the gap may be real, e.g. suspend),
//     backwards jumps are clamped like any other rollback.
//   - Because normalization depends on the global offset, callers rebuild
//     the timeline from scratch (all committed batches) whenever new late
//     data changes the offset estimate. That recompute is deterministic, so
//     incremental results always equal a full recompute.
package timesync

import "sort"

// EstimateOffset returns the median clock offset (serverMs-clientMs) from
// per-batch delta samples. It returns 0 for no samples.
func EstimateOffset(deltas []int64) int64 {
	if len(deltas) == 0 {
		return 0
	}
	sorted := append([]int64(nil), deltas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	// Average of two middle values; offsets are integer milliseconds.
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Normalizer maps client timestamps to a monotonic server-axis timeline.
type Normalizer struct {
	offset  int64
	maxJump int64
	maxSeen int64
	started bool
}

// NewNormalizer builds a Normalizer. maxJump<=0 disables jump flagging.
func NewNormalizer(offset int64, maxJump int64) *Normalizer {
	return &Normalizer{offset: offset, maxJump: maxJump}
}

// Offset returns the configured clock offset.
func (n *Normalizer) Offset() int64 { return n.offset }

// Normalize converts one client timestamp. Events must be fed in client-time
// order for the monotonic clamp to be meaningful. It returns the corrected
// server-axis timestamp, whether it was clamped due to a backwards move, and
// whether the move exceeded the configured jump threshold (in either
// direction).
func (n *Normalizer) Normalize(clientMs int64) (serverMs int64, clamped bool, jumped bool) {
	corrected := clientMs + n.offset
	if !n.started {
		n.started = true
		n.maxSeen = corrected
		return corrected, false, false
	}
	if n.maxJump > 0 {
		diff := corrected - n.maxSeen
		if diff < 0 {
			diff = -diff
		}
		jumped = diff > n.maxJump
	}
	if corrected < n.maxSeen {
		// Clock went backwards: never let the observed timeline flow back.
		clamped = true
		corrected = n.maxSeen
	} else {
		n.maxSeen = corrected
	}
	return corrected, clamped, jumped
}
