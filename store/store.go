// Package store persists accepted batches (append-only journal) and engine
// state snapshots. The journal is the source of truth: on restart the engine
// replays every committed batch and reconstructs dedupe sets, clock offsets
// and all window results deterministically, so an absorbed batch is never
// counted twice and no batch is lost. State snapshots are an optimization for
// restart speed and are written atomically (temp file + rename), which rules
// out torn/half-updated state.
package store

import (
	"sync"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// State is the recoverable engine snapshot.
type State struct {
	WindowRevisions map[string]int64   // WindowKey string -> revision
	Published       map[string]bool    // windows already published
	SeenBatches     map[string]bool    // dedupe: batch keys
	SeenEntries     map[string]bool    // dedupe: full entry identity keys
	SeenPayloads    map[string]bool    // dedupe: content-only payload keys
	ClockDeltas     map[string][]int64 // sessionID -> offset samples
	CommittedCount  int
	RevisionLog     []model.Revision // durable audit trail of window recomputes
}

// NewState returns an initialized empty state.
func NewState() *State {
	return &State{
		WindowRevisions: map[string]int64{},
		Published:       map[string]bool{},
		SeenBatches:     map[string]bool{},
		SeenEntries:     map[string]bool{},
		SeenPayloads:    map[string]bool{},
		ClockDeltas:     map[string][]int64{},
	}
}

// Store is the persistence contract.
type Store interface {
	// AppendBatch durably records one accepted batch before it is absorbed.
	AppendBatch(b model.Batch) error
	// ListBatches returns all committed batches in journal order.
	ListBatches() ([]model.Batch, error)
	// SaveState atomically persists a state snapshot.
	SaveState(s *State) error
	// LoadState reads the snapshot; ok=false when none exists yet.
	LoadState() (s *State, ok bool, err error)
}

// Memory is an in-memory Store (used by tests).
type Memory struct {
	mu      sync.Mutex
	Batches []model.Batch
	Snap    *State
}

// NewMemory creates an in-memory store.
func NewMemory() *Memory { return &Memory{} }

// AppendBatch records a batch in memory.
func (m *Memory) AppendBatch(b model.Batch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Batches = append(m.Batches, b)
	return nil
}

// ListBatches returns recorded batches in journal order.
func (m *Memory) ListBatches() ([]model.Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Batch, len(m.Batches))
	copy(out, m.Batches)
	return out, nil
}

// SaveState snapshots state in memory.
func (m *Memory) SaveState(s *State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Snap = cloneState(s)
	return nil
}

// LoadState reads the in-memory snapshot.
func (m *Memory) LoadState() (*State, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Snap == nil {
		return nil, false, nil
	}
	return cloneState(m.Snap), true, nil
}

func cloneState(s *State) *State {
	c := NewState()
	for k, v := range s.WindowRevisions {
		c.WindowRevisions[k] = v
	}
	for k, v := range s.Published {
		c.Published[k] = v
	}
	for k, v := range s.SeenBatches {
		c.SeenBatches[k] = v
	}
	for k, v := range s.SeenEntries {
		c.SeenEntries[k] = v
	}
	for k, v := range s.SeenPayloads {
		c.SeenPayloads[k] = v
	}
	for k, v := range s.ClockDeltas {
		c.ClockDeltas[k] = append([]int64(nil), v...)
	}
	c.RevisionLog = append(c.RevisionLog, s.RevisionLog...)
	c.CommittedCount = s.CommittedCount
	return c
}
