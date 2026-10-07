package aggregate

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/session"
)

func TestBuildInteractionAttributionAndMeasures(t *testing.T) {
	cfg := model.DefaultConfig()
	events := []model.Event{
		{Key: "s", Kind: model.KindMark, Name: "s", Start: 0, End: 0},
		{Key: "e", Kind: model.KindMark, Name: "e", Start: 100, End: 100},
		{Key: "m", Kind: model.KindMeasure, Name: "api", Start: 1, End: 1, Detail: "s,e"},
		// incomplete measure must not touch the complete sketch
		{Key: "m2", Kind: model.KindMeasure, Name: "api", Start: 2, End: 2, Detail: "s,noend"},
		// long task overlapping an interaction window
		{Key: "lt", Kind: model.KindLongTask, Start: 200, End: 350},
		{Key: "ix", Kind: model.KindInteraction, Source: "tap1", Start: 220, End: 300},
		// frames
		{Key: "f1", Kind: model.KindFrame, Start: 400, End: 450, Fps: 12},
		{Key: "f2", Kind: model.KindFrame, Start: 450, End: 500, Fps: 10},
		{Key: "f3", Kind: model.KindFrame, Start: 500, End: 550, Fps: 8},
	}
	seg := session.NavSegment{NavID: "n1", Segment: 0, Events: events, StartMs: 0, EndMs: 550}
	key := model.WindowKey{UserID: "u", SessionID: "s", PageID: "p", NavID: "n1", Segment: 0, TumbleIdx: -1}
	w := Build(key, seg, cfg, 999)

	api := w.Measures["api"]
	if api.Complete.Count != 1 || api.Complete.Mean != 100 {
		t.Fatalf("complete sketch must contain only valid measure: %+v", api.Complete)
	}
	if api.Incomplete[model.SegNoEnd] != 1 || api.IncompleteTotal != 1 {
		t.Fatalf("incomplete must be counted by reason: %+v", api.Incomplete)
	}

	lt := w.LongTasks
	if lt.TaskCount != 1 || lt.TotalBlockedMs != 150 {
		t.Fatalf("unioned long task wrong: %+v", lt)
	}
	if lt.ByWindow["ixn:tap1"] != 80 {
		t.Fatalf("interaction share = %v want 80", lt.ByWindow["ixn:tap1"])
	}
	if lt.ByWindow["nav:n1"] != 70 {
		t.Fatalf("nav remainder = %v want 70 (200-220 + 300-350)", lt.ByWindow["nav:n1"])
	}
	if lt.ByWindow["ixn:tap1"]+lt.ByWindow["nav:n1"] != lt.TotalBlockedMs {
		t.Fatal("attribution shares must sum to total blocked time")
	}
	if w.Frames.JankEvents != 1 || w.Frames.DroppedFrames != 3 {
		t.Fatalf("frame stats wrong: %+v", w.Frames)
	}
}

func TestTumbleStableEpochBuckets(t *testing.T) {
	seg := session.NavSegment{NavID: "n1", Segment: 0, TumbleIdx: -1,
		Events: []model.Event{
			{Key: "a", Kind: model.KindMark, Start: 100, End: 100},
			{Key: "b", Kind: model.KindMark, Start: 999, End: 999},
			{Key: "c", Kind: model.KindMark, Start: 1100, End: 1100},
		}}
	subs := Tumble(seg, 1000)
	if len(subs) != 2 {
		t.Fatalf("expected 2 tumble buckets, got %d", len(subs))
	}
	if subs[0].TumbleIdx != 0 || subs[1].TumbleIdx != 1 {
		t.Fatalf("bucket ids must be epoch-aligned: %d,%d", subs[0].TumbleIdx, subs[1].TumbleIdx)
	}
	if len(subs[0].Events) != 2 || len(subs[1].Events) != 1 {
		t.Fatalf("event bucketing wrong: %d,%d (want bucket0={100,999}, bucket1={1100})", len(subs[0].Events), len(subs[1].Events))
	}
	if Tumble(seg, 0) != nil {
		t.Fatal("tumble disabled must produce no sub-windows")
	}
}
