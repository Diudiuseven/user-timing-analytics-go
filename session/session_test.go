package session

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func ev(k string, kind model.EntryKind, start int64, name string, life model.LifecycleType, persisted bool) model.Event {
	return model.Event{Key: k, Kind: kind, Name: name, Start: start, End: start, Life: life, Persisted: persisted}
}

func TestSuspendResumeSplitsSegments(t *testing.T) {
	events := []model.Event{
		ev("m1", model.KindMark, 100, "a", "", false),
		ev("f", model.KindLifecycle, 200, "", model.LifeFreeze, false),
		ev("r", model.KindLifecycle, 300, "", model.LifeResume, false),
		ev("m2", model.KindMark, 400, "b", "", false),
	}
	tl := Build("u", "s", "p", events, nil)
	if len(tl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(tl.Segments))
	}
	if tl.Segments[0].Segment != 0 || tl.Segments[1].Segment != 1 {
		t.Fatalf("segment ids = %d,%d", tl.Segments[0].Segment, tl.Segments[1].Segment)
	}
	if tl.Segments[0].Events[len(tl.Segments[0].Events)-1].Life != model.LifeFreeze {
		t.Fatal("freeze must stay in old segment")
	}
	if tl.Segments[1].Events[0].Life != model.LifeResume {
		t.Fatal("resume must open new segment")
	}
}

func TestBFCacheRestore(t *testing.T) {
	events := []model.Event{
		ev("h", model.KindLifecycle, 10, "", model.LifePageHide, false),
		ev("ps", model.KindLifecycle, 20, "", model.LifePageShow, true),
		ev("bf", model.KindLifecycle, 30, "", model.LifeBFCacheRestore, false),
		ev("m", model.KindMark, 40, "x", "", false),
	}
	tl := Build("u", "s", "p", events, nil)
	// segment0: pagehide; segment1: pageshow(persisted); segment2: bfcache_restore + mark
	if len(tl.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(tl.Segments))
	}
}

func TestImplicitResumeAfterSuspend(t *testing.T) {
	events := []model.Event{
		ev("f", model.KindLifecycle, 10, "", model.LifeFreeze, false),
		ev("m", model.KindMark, 20, "x", "", false),
	}
	tl := Build("u", "s", "p", events, nil)
	if len(tl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (implicit resume)", len(tl.Segments))
	}
}

func TestNavIDAndMarkSwitchNavigation(t *testing.T) {
	events := []model.Event{
		ev("a", model.KindMark, 1, "x", "", false),
		ev("b", model.KindMark, 2, NavMarkName, "", false),
		ev("c", model.KindMark, 3, "y", "", false),
	}
	batchNav := map[string]string{}
	tl := Build("u", "s", "p", events, batchNav)
	if len(tl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 navigations", len(tl.Segments))
	}
	if tl.Segments[0].NavID == tl.Segments[1].NavID {
		t.Fatal("SPA nav mark must start a new navigation")
	}
}

func TestAdvertisedNavIDSwitch(t *testing.T) {
	events := []model.Event{
		{Key: "a", Kind: model.KindMark, Name: "x", Start: 1, End: 1, BatchID: "b1"},
		{Key: "b", Kind: model.KindMark, Name: "y", Start: 2, End: 2, BatchID: "b2"},
	}
	tl := Build("u", "s", "p", events, map[string]string{"b1": "n1", "b2": "n2"})
	if len(tl.Segments) != 2 || tl.Segments[0].NavID != "n1" || tl.Segments[1].NavID != "n2" {
		t.Fatalf("advertised NavID must split: %+v", tl.Segments)
	}
}

func TestOutOfOrderConverges(t *testing.T) {
	events := []model.Event{
		ev("m2", model.KindMark, 400, "b", "", false),
		ev("r", model.KindLifecycle, 300, "", model.LifeResume, false),
		ev("f", model.KindLifecycle, 200, "", model.LifeFreeze, false),
		ev("m1", model.KindMark, 100, "a", "", false),
	}
	tl := Build("u", "s", "p", events, nil)
	if len(tl.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 regardless of arrival order", len(tl.Segments))
	}
	if tl.Segments[0].Events[0].Key != "m1" {
		t.Fatal("timeline must be chronological after rebuild")
	}
}
