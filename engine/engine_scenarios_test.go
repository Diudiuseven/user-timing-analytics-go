package engine

import (
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/store"
)

// 4. Client clock rolls backwards: the corrected timeline never reverses.
func TestClockRollbackTimelineMonotonic(t *testing.T) {
	cfg := model.DefaultConfig()
	e, _, _ := testEngine(t, cfg)
	// Offset ~1000ms (server 5000, last client entry 4000).
	if _, err := e.Ingest(batch("b1", "n1", 5_000,
		mark("a", 4_000), mark("b", 4_100))); err != nil {
		t.Fatal(err)
	}
	// Client clock jumps backwards to 2000; a further batch also contains a
	// normal late entry at 4200.
	if _, err := e.Ingest(batch("b2", "n1", 5_200,
		mark("c", 2_000), mark("d", 4_200))); err != nil {
		t.Fatal(err)
	}
	// Feed a chronological client stream containing a backwards step:
	// 4000, 4100, then the clock rolls back to 2000 and later ticks to 4200.
	got := normalizedStarts(e, "s", []int64{4_000, 4_100, 2_000, 4_200})
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("normalized axis reversed: %v (raw inputs 4000,4100,2000,4200)", got)
		}
	}
	if got[2] != got[1] {
		t.Fatalf("rolled-back timestamp must clamp to the last seen time %d, got %d", got[1], got[2])
	}
	if got[3] <= got[1] {
		t.Fatalf("normal late entry must advance beyond the pre-rollback mark: %v", got)
	}
}

// 5. Late data that changes a published window yields a full recompute and a
// monotonic revision record (never a partial field update).
func TestLateDataRevisionOnPublishedWindow(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.CheckpointAfter = 1
	dir := t.TempDir()
	st0, _ := store.OpenFile(filepath.Join(dir, "db"))
	e0, err := New(cfg, st0)
	if err != nil {
		t.Fatal(err)
	}
	e := e0.WithClock(func() int64 { return 7_777 })
	_ = e
	if _, err := e.Ingest(batch("b1", "n1", 2_000,
		mark("s", 1_000), mark("e", 1_100), measureE("api", 1_050, "s,e"))); err != nil {
		t.Fatal(err)
	}
	key := e.Results()[0].Key
	if !e.Publish(key) {
		t.Fatal("publish")
	}
	before := e.Results()[0]
	if before.Measures["api"].Complete.Count != 1 {
		t.Fatal("precondition")
	}

	// A late second complete measure changes the window content.
	rep, err := e.Ingest(batch("b2", "n1", 9_000,
		mark("s2", 1_200), mark("e2", 1_300), measureE("api", 1_250, "s2,e2")))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Revisions) != 1 {
		t.Fatalf("expected one revision, got %+v", rep.Revisions)
	}
	rv := rep.Revisions[0]
	if rv.ToRevision != 1 || rv.FromRevision != 0 {
		t.Fatalf("revision numbers wrong: %+v", rv)
	}
	after := e.Results()[0]
	if after.Revision != 1 {
		t.Fatalf("window must carry new revision, got %d", after.Revision)
	}
	if after.Measures["api"].Complete.Count != 2 {
		t.Fatal("recomputed window must contain both measures (full, not partial)")
	}
	if after.WindowStart == 0 || after.WindowEnd == 0 {
		t.Fatal("all fields must be present after recompute")
	}

	// The revision audit trail and monotonic revision number must survive a
	// process restart via the durable journal + state snapshot.
	st1, _ := store.OpenFile(filepath.Join(dir, "db"))
	e1, err := New(cfg, st1)
	if err != nil {
		t.Fatal(err)
	}
	log := e1.RevisionLog()
	if len(log) != 1 || log[0].ToRevision != 1 || log[0].Window != key {
		t.Fatalf("revision log must survive restart: %+v", log)
	}
	if e1.Results()[0].Revision != 1 {
		t.Fatalf("revision number must not regress after restart: %d", e1.Results()[0].Revision)
	}
}

// 6. Incremental ingestion equals one-shot full ingestion, measure by measure.
func TestIncrementalEqualsFull(t *testing.T) {
	cfg := model.DefaultConfig()
	all := []model.Batch{
		batch("b1", "n1", 2_000, mark("s", 1_000), mark("e", 1_120), measureE("api", 1_010, "s,e"), lt(1_500, 90)),
		batch("b2", "n1", 3_000, frame(1_600, 55, 18), mark("s2", 1_700), mark("e2", 1_800), measureE("api", 1_710, "s2,e2")),
		batch("b3", "n1", 4_000, life(1_900, model.LifeFreeze), life(2_000, model.LifeResume), frame(2_100, 20, 50)),
	}

	inc, _, _ := testEngine(t, cfg)
	for _, b := range all {
		if _, err := inc.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	// Now continue ingesting one more late batch incrementally and compare to
	// a fresh engine that ingested everything up front.
	late := batch("b4", "n1", 9_000, lt(1_550, 70), frame(2_200, 15, 66))
	if _, err := inc.Ingest(late); err != nil {
		t.Fatal(err)
	}

	full, _, _ := testEngine(t, cfg)
	for _, b := range append(all, late) {
		if _, err := full.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	if !sameResults(inc.Results(), full.Results()) {
		t.Fatalf("incremental pipeline diverged from full recompute\nINC: %s\nFULL:%s", dump(inc.Results()), dump(full.Results()))
	}
}

// 7. Restart recovery: journal replay resumes from the committed position,
// absorbed batches are not double counted, metrics are identical.
func TestRestartRecoveryFromJournal(t *testing.T) {
	dir := t.TempDir()
	cfg := model.DefaultConfig()
	cfg.CheckpointAfter = 1

	st, err := store.OpenFile(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e1, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	batches := []model.Batch{
		batch("b1", "n1", 2_000, mark("s", 1_000), mark("e", 1_120), measureE("api", 1_010, "s,e"), lt(1_500, 90)),
		batch("b2", "n1", 3_000, life(1_900, model.LifeFreeze), life(2_000, model.LifeResume), frame(2_100, 20, 50)),
	}
	for _, b := range batches {
		if _, err := e1.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	first := e1.Results()

	// Simulate process restart on the same directory.
	st2, err := store.OpenFile(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e2, err := New(cfg, st2)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResults(first, e2.Results()) {
		t.Fatal("recovered metrics must equal pre-restart metrics")
	}
	// Re-sending an already absorbed batch after restart must be a no-op.
	rep, err := e2.Ingest(batches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DuplicateBatch {
		t.Fatalf("absorbed batch must dedupe after restart: %+v", rep)
	}
	if !sameResults(first, e2.Results()) {
		t.Fatal("replayed batch after restart must not change metrics")
	}
	// And ingestion can continue from the committed position.
	if _, err := e2.Ingest(batch("b3", "n2", 4_000, mark("z", 3_000))); err != nil {
		t.Fatal(err)
	}
	if len(e2.Results()) != len(first)+1 {
		t.Fatalf("expected one additional window after resume, got %d vs %d", len(e2.Results()), len(first))
	}
}

// 8. Deterministic sampling: same seed -> same sessions kept, repeatedly.
func TestSamplingDeterministicAcrossEngines(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.SampleRate = 0.5
	cfg.SampleSeed = 99

	decisions := func() map[string]bool {
		e, _, _ := testEngine(t, cfg)
		out := map[string]bool{}
		for i := 0; i < 60; i++ {
			sess := "sess-" + itoa2(i)
			b := model.Batch{BatchID: "b-" + itoa2(i), UserID: "u", SessionID: sess, PageID: "p",
				ServerTimeMs: 1000, Entries: []model.Entry{mark("x", 900)}}
			rep, err := e.Ingest(b)
			if err != nil {
				t.Fatal(err)
			}
			out[sess] = rep.AcceptedBatch
		}
		return out
	}
	d1 := decisions()
	d2 := decisions()
	for k, v := range d1 {
		if d2[k] != v {
			t.Fatalf("sampling not reproducible for %s: %v vs %v", k, v, d2[k])
		}
	}
}

func itoa2(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// 9. Different pages/sessions are independent observation units; identical
// mark/measure names in them must not cross-resolve.
func TestCrossPageIsolation(t *testing.T) {
	cfg := model.DefaultConfig()
	e, _, _ := testEngine(t, cfg)
	b1 := batch("b1", "n1", 2_000, mark("s", 1_000), mark("e", 1_100), measureE("api", 1_050, "s,e"))
	b1.PageID = "pageA"
	b2 := batch("b2", "n1", 3_000, mark("s", 1_000), measureE("api", 1_050, "s,e")) // no end mark
	b2.PageID = "pageB"
	if _, err := e.Ingest(b1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ingest(b2); err != nil {
		t.Fatal(err)
	}
	completeByPage := map[string]int{}
	for _, w := range e.Results() {
		if a, ok := w.Measures["api"]; ok {
			completeByPage[w.Key.PageID] = a.Complete.Count
		}
	}
	if completeByPage["pageA"] != 1 || completeByPage["pageB"] != 0 {
		t.Fatalf("mark from pageA must not complete measure in pageB: %+v", completeByPage)
	}
}

// 10. Concurrent ingest is serialized; duplicate batches across goroutines
// collapse and results stay consistent (run with -race).
func TestConcurrentIngest(t *testing.T) {
	cfg := model.DefaultConfig()
	e, _, _ := testEngine(t, cfg)
	const n = 20
	errs := make(chan error, n*2)
	done := make(chan struct{}, n*2)
	for i := 0; i < n; i++ {
		mkBatch := func(i int) model.Batch {
			base := int64(9_000 + i*100)
			return batch("parallel-"+itoa2(i), "npar", int64(10_000+i),
				mark("s-"+itoa2(i), base), mark("e-"+itoa2(i), base+100),
				measureE("m", base+50, "s-"+itoa2(i)+",e-"+itoa2(i)))
		}
		go func(i int) { _, err := e.Ingest(mkBatch(i)); errs <- err; done <- struct{}{} }(i)
		go func(i int) { _, err := e.Ingest(mkBatch(i)); errs <- err; done <- struct{}{} }(i) // retry
	}
	for i := 0; i < n*2; i++ {
		<-done
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	// All 20 parallel batches share navigation npar; duplicates absorbed once.
	var measureCount int
	for _, w := range e.Results() {
		if a, ok := w.Measures["m"]; ok {
			measureCount += a.Complete.Count
		}
	}
	if measureCount != n {
		t.Fatalf("expected %d unique measures, got %d", n, measureCount)
	}
}

// 11. Tumbling windows use stable epoch buckets: a late entry lands in the
// correct existing bucket, and both segment-level and tumble windows exist.
func TestTumblingWindows(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.TumbleMs = 1000
	e, _, _ := testEngine(t, cfg)
	// Zero clock offset: server receive time equals the batch's latest entry.
	if _, err := e.Ingest(batch("b1", "n1", 900,
		frame(100, 60, 16), frame(900, 60, 16))); err != nil {
		t.Fatal(err)
	}
	// Late entries that belong to epoch bucket 0 ([0,1000)).
	if _, err := e.Ingest(batch("b2", "n1", 532,
		frame(500, 20, 50), frame(516, 15, 66), frame(532, 12, 83))); err != nil {
		t.Fatal(err)
	}
	var segLevel, tumble0, tumble1 int
	for _, w := range e.Results() {
		switch {
		case w.Key.TumbleIdx == -1:
			segLevel++
		case w.Key.TumbleIdx == 0:
			tumble0 = w.Frames.DroppedFrames
		case w.Key.TumbleIdx == 1:
			tumble1 = w.Frames.DroppedFrames
		}
	}
	if segLevel != 1 {
		t.Fatalf("expected one segment-level window, got %d", segLevel)
	}
	if tumble0 != 3 {
		t.Fatalf("late frames must fold into epoch bucket 0: dropped=%d", tumble0)
	}
	if tumble1 != 0 {
		t.Fatalf("bucket 1 holds the healthy frame at 900 only, dropped=%d", tumble1)
	}
}
