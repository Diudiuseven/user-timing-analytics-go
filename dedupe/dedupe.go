// Package dedupe provides stable-key idempotency for batches and entries.
//
// Retried uploads carry the same batch ID and are absorbed exactly once.
// Individual entries are keyed by (batchID, kind, name, startMs, durMs,
// life/source, sequence-within-batch); identical entries that appear in
// different batches are also collapsed on the same key. The key composition
// is pure and order-independent: two reports that are byte-identical in
// content always produce the same key, and the first accepted occurrence
// wins.
package dedupe

import (
	"strconv"
	"strings"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// BatchKey is the stable identity of an upload.
func BatchKey(userID, batchID string) string {
	return userID + "|b|" + batchID
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// EntryKey is the stable identity of one entry within a batch. idx breaks
// ties between two genuinely distinct entries that happen to carry the same
// fields (e.g. two identical marks), keeping first-occurrence ordering
// deterministic.
func EntryKey(batchID string, idx int, e model.Entry) string {
	return batchID + "|e|" + strconv.Itoa(idx) + "|" + PayloadKey(e)
}

// PayloadKey identifies an entry by its semantic content only (no batch id,
// no in-batch position). Identical entries resent inside a different batch id
// collapse on this key, which covers clients that repackage retries into new
// uploads.
func PayloadKey(e model.Entry) string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	b.WriteByte('|')
	b.WriteString(e.Name)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(e.StartMs, 10))
	b.WriteByte('|')
	b.WriteString(ftoa(e.DurMs))
	b.WriteByte('|')
	b.WriteString(string(e.Life))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(e.Persisted))
	b.WriteByte('|')
	b.WriteString(ftoa(e.Fps))
	b.WriteByte('|')
	b.WriteString(e.Source)
	return b.String()
}

// Set is a thread-unsafe set of already-accepted keys (the engine serializes
// access). It is reconstructable from the committed journal, so dedup state
// survives restarts without any extra persistence.
type Set struct{ seen map[string]struct{} }

// NewSet creates an empty Set.
func NewSet() *Set { return &Set{seen: make(map[string]struct{})} }

// Add records a key and reports whether it was newly seen. A false result
// means the key (and therefore the batch or entry) is a duplicate.
func (s *Set) Add(key string) bool {
	if _, ok := s.seen[key]; ok {
		return false
	}
	s.seen[key] = struct{}{}
	return true
}

// Has reports whether a key was already accepted.
func (s *Set) Has(key string) bool {
	_, ok := s.seen[key]
	return ok
}

// Len returns the number of accepted keys.
func (s *Set) Len() int { return len(s.seen) }
