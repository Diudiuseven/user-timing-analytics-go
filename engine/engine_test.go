package engine

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/aggregate"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/store"
)

func testEngine(t *testing.T, cfg model.Config) (*Engine, *store.Memory, func() int64) {
	t.Helper()
	clock := int64(1_000_000)
	now := func() int64 { return clock }
	st := store.NewMemory()
	e, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	e.WithClock(now)
	return e, st, func() int64 { return clock }
}

func mark(name string, start int64, detail ...string) model.Entry {
	d := ""
	if len(detail) > 0 {
		d = detail[0]
	}
	return model.Entry{Kind: model.KindMark, Name: name, StartMs: start, Detail: d}
}
func measureE(name string, start int64, detail string) model.Entry {
	return model.Entry{Kind: model.KindMeasure, Name: name, StartMs: start, Detail: detail}
}
func lt(start int64, dur float64) model.Entry {
	return model.Entry{Kind: model.KindLongTask, StartMs: start, DurMs: dur}
}
func frame(start int64, fps, dur float64) model.Entry {
	return model.Entry{Kind: model.KindFrame, StartMs: start, DurMs: dur, Fps: fps}
}
func life(start int64, l model.LifecycleType, persisted ...bool) model.Entry {
	p := false
	if len(persisted) > 0 {
		p = persisted[0]
	}
	return model.Entry{Kind: model.KindLifecycle, StartMs: start, Life: l, Persisted: p}
}
func batch(id, nav string, serverMs int64, entries ...model.Entry) model.Batch {
	return model.Batch{BatchID: id, UserID: "u", SessionID: "s", PageID: "p", NavID: nav, ServerTimeMs: serverMs, Entries: entries}
}

func resultByName(e *Engine, measureName string) bool {
	for _, w := range e.Results() {
		if a, ok := w.Measures[measureName]; ok {
			return a.Complete.Count > 0
		}
	}
	return false
}

func sameResults(a, b []*aggregate.WindowResult) bool {
	if len(a) != len(b) {
		return false
	}
	ca := map[string]string{}
	for _, w := range a {
		ca[string(keyString(w.Key))] = canonical(w)
	}
	for _, w := range b {
		if ca[string(keyString(w.Key))] != canonical(w) {
			return false
		}
	}
	return true
}

var _ = resultByName

// 1. Out-of-order vs in-order converge; duplicates don't change results.
func TestOutOfOrderDuplicatesConverge(t *testing.T) {
	cfg := model.DefaultConfig()

	ordered := []model.Batch{
		batch("b1", "n1", 1_000_200, mark("s", 100), mark("e", 220), measureE("api", 110, "s,e")),
		batch("b2", "n1", 1_000_300, lt(1000, 120)),
	}
	e1, _, _ := testEngine(t, cfg)
	for _, b := range ordered {
		if _, err := e1.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	base := e1.Results()

	// Same data, shuffled arrival order.
	e2, _, _ := testEngine(t, cfg)
	shuffled := []model.Batch{ordered[1], ordered[0]}
	for _, b := range shuffled {
		if _, err := e2.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	if !sameResults(base, e2.Results()) {
		t.Fatal("out-of-order arrival must converge to chronological results")
	}

	// Duplicate batch retried: accepted flag false, results unchanged.
	rep, err := e1.Ingest(ordered[0])
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DuplicateBatch || rep.AcceptedBatch {
		t.Fatalf("retry must be reported duplicate: %+v", rep)
	}
	if !sameResults(base, e1.Results()) {
		t.Fatal("duplicate batch must not change any metric")
	}

	// Duplicate entries inside a new batch id are dropped on stable keys.
	dup := batch("b3", "n1", 1_000_400, lt(1000, 120) /*same payload*/)
	rep, _ = e1.Ingest(dup)
	if rep.DroppedEntries != 1 || rep.AcceptedEntries != 0 {
		t.Fatalf("duplicate entry must be dropped: %+v", rep)
	}
	if !sameResults(base, e1.Results()) {
		t.Fatal("duplicate long task entry must not double-count blocked time")
	}
}

// 2. Late data belonging to an existing window folds in correctly.
func TestLateArrivalFoldsIn(t *testing.T) {
	cfg := model.DefaultConfig()
	e, _, _ := testEngine(t, cfg)
	if _, err := e.Ingest(batch("b1", "n1", 1_000_200, mark("s", 100), measureE("api", 110, "s,e"))); err != nil {
		t.Fatal(err)
	}
	if resultByName(e, "api") {
		t.Fatal("measure must be incomplete while end mark is late")
	}
	// Late end mark arrives in a later batch with a later server time.
	if _, err := e.Ingest(batch("b2", "n1", 1_000_500, mark("e", 260))); err != nil {
		t.Fatal(err)
	}
	var complete int
	var dur float64
	for _, w := range e.Results() {
		if a, ok := w.Measures["api"]; ok {
			complete = a.Complete.Count
			dur = a.Complete.Mean
		}
	}
	if complete != 1 || dur != 160 {
		t.Fatalf("late end mark must complete the measure: count=%d dur=%v", complete, dur)
	}
}

// 3. Navigation and suspension split windows; resume never stitches.
func TestNavigationAndSuspendSplit(t *testing.T) {
	cfg := model.DefaultConfig()
	e, _, _ := testEngine(t, cfg)
	batches := []model.Batch{
		batch("b1", "n1", 1_000_200,
			mark("s", 100), mark("e", 150), measureE("m", 110, "s,e"),
			life(200, model.LifeFreeze)),
		batch("b2", "n1", 1_000_400,
			life(300, model.LifeResume),
			mark("s2", 320), mark("e2", 400), measureE("m2", 330, "s2,e2")),
		batch("b3", "n2", 1_000_600,
			mark("x", 500)),
	}
	for _, b := range batches {
		if _, err := e.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	res := e.Results()
	if len(res) != 3 {
		t.Fatalf("windows = %d, want 3 (2 segments of n1 + n2)", len(res))
	}
	navs := map[string]int{}
	for _, w := range res {
		navs[w.Key.NavID]++
	}
	if navs["n1"] != 2 || navs["n2"] != 1 {
		t.Fatalf("nav window counts wrong: %+v", navs)
	}
}
