// Package engine ties ingestion, de-duplication, sampling, clock
// normalization, session/navigation segmentation and metric aggregation into
// one recoverable, deterministic pipeline.
//
// # Consistency model
//
// Every Ingest call (a) drops duplicate batches/entries on stable keys,
// (b) applies deterministic session-level sampling, (c) durably appends the
// accepted batch to the journal, then (d) runs a full reconciliation: every
// committed batch is re-normalized and re-aggregated from scratch. Full
// reconciliation is the single code path for both "incremental" updates and
// full recomputation, so per-window results of incremental ingest are
// provably identical to a cold-start full recompute.
//
// When late data changes an already-published window, the engine never
// applies a partial in-place patch. It builds the complete new result, bumps
// a monotonic revision and appends a Revision audit record; a window result
// is always either the old complete value or the new complete value.
//
// Restart recovery replays the journal: already absorbed batch IDs are in
// the journal itself, so replay is idempotent and resumes from the last
// committed position.
package engine

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Diudiuseven/user-timing-analytics-go/aggregate"
	"github.com/Diudiuseven/user-timing-analytics-go/dedupe"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/sampling"
	"github.com/Diudiuseven/user-timing-analytics-go/session"
	"github.com/Diudiuseven/user-timing-analytics-go/store"
	"github.com/Diudiuseven/user-timing-analytics-go/timesync"
)

// IngestReport describes what happened to one upload.
type IngestReport struct {
	AcceptedBatch   bool
	DuplicateBatch  bool
	SampledOut      bool
	AcceptedEntries int
	DroppedEntries  int
	Revisions       []model.Revision
}

// Engine is the analytics pipeline.
type Engine struct {
	mu      sync.Mutex
	cfg     model.Config
	store   store.Store
	decider *sampling.Decider
	now     func() int64
	state   *store.State
	windows map[windowKeyJSON]*aggregate.WindowResult
}

// New constructs an Engine and recovers committed state from st.
func New(cfg model.Config, st store.Store) (*Engine, error) {
	e := &Engine{
		cfg:     cfg,
		store:   st,
		decider: sampling.NewDecider(cfg.SampleRate, cfg.SampleSeed),
		now:     func() int64 { return time.Now().UnixMilli() },
		state:   store.NewState(),
		windows: map[windowKeyJSON]*aggregate.WindowResult{},
	}
	if snap, ok, err := st.LoadState(); err != nil {
		return nil, err
	} else if ok {
		e.state = snap
	}
	if _, err := e.reconcileLocked("restart-replay", false); err != nil {
		return nil, err
	}
	return e, nil
}

// WithClock injects a server clock (tests/reproducibility).
func (e *Engine) WithClock(now func() int64) *Engine {
	e.now = now
	return e
}

// Ingest accepts one batch upload.
func (e *Engine) Ingest(b model.Batch) (IngestReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var rep IngestReport
	bkey := dedupe.BatchKey(b.UserID, b.BatchID)
	if e.state.SeenBatches[bkey] {
		rep.DuplicateBatch = true
		return rep, nil
	}
	if !e.decider.Keep("session:" + b.UserID + "|" + b.SessionID) {
		rep.SampledOut = true
		// Still remember the batch so a retry doesn't flip the decision.
		e.state.SeenBatches[bkey] = true
		return rep, nil
	}

	// Entry-level dedupe before persistence: first on the full identity key
	// (same batch retried), then on the content-only payload key (identical
	// entry repackaged into another batch id).
	kept := b.Entries[:0]
	for i, en := range b.Entries {
		ekey := dedupe.EntryKey(b.BatchID, i, en)
		pkey := dedupe.PayloadKey(en)
		if e.state.SeenEntries[ekey] || e.state.SeenPayloads[pkey] {
			rep.DroppedEntries++
			continue
		}
		e.state.SeenEntries[ekey] = true
		e.state.SeenPayloads[pkey] = true
		kept = append(kept, en)
		rep.AcceptedEntries++
	}
	b.Entries = kept

	if b.ServerTimeMs == 0 {
		b.ServerTimeMs = e.now()
	}
	if err := e.store.AppendBatch(b); err != nil {
		return rep, err
	}
	e.state.SeenBatches[bkey] = true
	// Only batches that actually carry data contribute a clock sample: an
	// all-duplicate batch has no meaningful client send time.
	if b.ServerTimeMs > 0 && len(b.Entries) > 0 {
		var clientSend int64
		for _, en := range b.Entries {
			if en.StartMs > clientSend {
				clientSend = en.StartMs
			}
		}
		e.state.ClockDeltas[b.SessionID] = append(e.state.ClockDeltas[b.SessionID], b.ServerTimeMs-clientSend)
	}

	revs, err := e.reconcileLocked("ingest:"+b.BatchID, true)
	rep.Revisions = revs
	rep.AcceptedBatch = true
	if err == nil {
		e.state.CommittedCount++
		due := e.cfg.CheckpointAfter > 0 && e.state.CommittedCount%e.cfg.CheckpointAfter == 0
		if due || len(revs) > 0 {
			// Persist promptly on schedule or whenever the revision audit log
			// gained entries, so recomputations never silently disappear.
			err = e.store.SaveState(e.state)
		}
	}
	return rep, err
}

type pageGroup struct {
	userID, sessionID, pageID string
	batches                   []model.Batch
}

type windowKeyJSON string

func keyString(k model.WindowKey) windowKeyJSON {
	data, _ := json.Marshal(k)
	return windowKeyJSON(data)
}

// reconcileLocked rebuilds every window from the committed journal.
func (e *Engine) reconcileLocked(reason string, durable bool) ([]model.Revision, error) {
	batches, err := e.store.ListBatches()
	if err != nil {
		return nil, err
	}

	// Group accepted batches by (user, session, page), journal order stable.
	groups := map[string]*pageGroup{}
	var order []string
	for _, b := range batches {
		gk := b.UserID + "|" + b.SessionID + "|" + b.PageID
		g := groups[gk]
		if g == nil {
			g = &pageGroup{userID: b.UserID, sessionID: b.SessionID, pageID: b.PageID}
			groups[gk] = g
			order = append(order, gk)
		}
		g.batches = append(g.batches, b)
	}

	fresh := map[windowKeyJSON]*aggregate.WindowResult{}
	var revs []model.Revision
	now := e.now()

	for _, gk := range order {
		g := groups[gk]

		// Deterministic order-independent offset from all samples.
		offset := timesync.EstimateOffset(e.state.ClockDeltas[g.sessionID])
		norm := timesync.NewNormalizer(offset, e.cfg.MaxClockJumpMs)

		// Flatten entries with global stable keys, keeping raw client time.
		type flat struct {
			ev       model.Event
			navID    string
			batchID  string
			clientMs int64
			durMs    float64
		}
		var flats []flat
		for _, b := range g.batches {
			for i, en := range b.Entries {
				flats = append(flats, flat{
					ev: model.Event{
						Key:       dedupe.EntryKey(b.BatchID, i, en),
						Kind:      en.Kind,
						Name:      en.Name,
						Life:      en.Life,
						Persisted: en.Persisted,
						Fps:       en.Fps,
						Source:    en.Source,
						Detail:    en.Detail,
						BatchID:   b.BatchID,
					},
					navID: b.NavID, batchID: b.BatchID,
					clientMs: en.StartMs, durMs: en.DurMs,
				})
			}
		}
		// Step 1: chronological client-time stream into the monotonic
		// normalizer, so out-of-order arrival cannot trigger bogus clamps.
		sort.SliceStable(flats, func(i, j int) bool {
			if flats[i].clientMs != flats[j].clientMs {
				return flats[i].clientMs < flats[j].clientMs
			}
			return flats[i].ev.Key < flats[j].ev.Key
		})
		for i := range flats {
			s, _, _ := norm.Normalize(flats[i].clientMs)
			flats[i].ev.Start = s
			flats[i].ev.End = s + msDur(flats[i].durMs)
		}
		// Step 2: order the corrected events for timeline reconstruction.
		sort.SliceStable(flats, func(i, j int) bool {
			if flats[i].ev.Start != flats[j].ev.Start {
				return flats[i].ev.Start < flats[j].ev.Start
			}
			return flats[i].ev.Key < flats[j].ev.Key
		})

		events := make([]model.Event, len(flats))
		batchNav := map[string]string{}
		for i, f := range flats {
			events[i] = f.ev
			if f.navID != "" {
				batchNav[f.batchID] = f.navID
			}
		}

		tl := session.Build(g.userID, g.sessionID, g.pageID, events, batchNav)

		// emitWindow builds one window (segment-level or a tumbling child),
		// applies revision rules against the previous complete result, and
		// installs it into the fresh window set as one atomic value.
		emitWindow := func(seg session.NavSegment, tumbleIdx int64) {
			k := model.WindowKey{
				UserID: g.userID, SessionID: g.sessionID, PageID: g.pageID,
				NavID: seg.NavID, Segment: seg.Segment, TumbleIdx: tumbleIdx,
			}
			w := aggregate.Build(k, seg, e.cfg, now)
			ks := keyString(k)
			storedRev := e.state.WindowRevisions[string(ks)]
			w.Revision = storedRev
			if old, published := e.windows[ks], e.state.Published[string(ks)]; old != nil && published {
				if !equalWindow(old, w) {
					newRev := old.Revision + 1
					if newRev <= storedRev {
						newRev = storedRev + 1
					}
					w.Revision = newRev
					w.Closed = old.Closed
					e.state.WindowRevisions[string(ks)] = newRev
					revs = append(revs, model.Revision{
						Window: k, FromRevision: old.Revision, ToRevision: newRev,
						Reason: reason, AtMs: now,
					})
				} else {
					w.Revision = old.Revision
					w.Closed = old.Closed
				}
			}
			fresh[ks] = w
		}

		for _, seg := range tl.Segments {
			emitWindow(seg, -1)
			for _, sub := range aggregate.Tumble(seg, e.cfg.TumbleMs) {
				emitWindow(sub, sub.TumbleIdx)
			}
		}
	}
	e.windows = fresh
	if durable {
		e.state.RevisionLog = append(e.state.RevisionLog, revs...)
	}
	sort.Slice(revs, func(i, j int) bool {
		if revs[i].AtMs != revs[j].AtMs {
			return revs[i].AtMs < revs[j].AtMs
		}
		return string(keyString(revs[i].Window)) < string(keyString(revs[j].Window))
	})
	return revs, nil
}

// Results returns a snapshot copy of all current window results.
func (e *Engine) Results() []*aggregate.WindowResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*aggregate.WindowResult, 0, len(e.windows))
	for _, w := range e.windows {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(keyString(out[i].Key)) < string(keyString(out[j].Key))
	})
	return out
}

// Publish marks a window published; later changes to it produce revisions.
func (e *Engine) Publish(k model.WindowKey) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ks := keyString(k)
	if _, ok := e.windows[ks]; !ok {
		return false
	}
	if !e.state.Published[string(ks)] {
		e.state.Published[string(ks)] = true
		_ = e.store.SaveState(e.state)
	}
	return true
}

// RevisionLog returns a copy of the durable audit trail of published-window
// recomputes (survives restarts).
func (e *Engine) RevisionLog() []model.Revision {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]model.Revision(nil), e.state.RevisionLog...)
	return out
}

// Checkpoint forces a durable state snapshot.
func (e *Engine) Checkpoint() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.SaveState(e.state)
}

func msDur(d float64) int64 {
	v := int64(d + 0.5)
	if v < 0 {
		return 0
	}
	return v
}
