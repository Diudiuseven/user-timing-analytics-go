package session

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/clock"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func ce(kind model.EntryKind, name, page, nav string, t int64) clock.CorrectedEntry {
	return clock.CorrectedEntry{
		Entry:      model.Entry{Kind: kind, Name: name, PageID: page, NavID: nav, ClientTime: t},
		ServerTime: t,
	}
}

func TestNavigationSplit(t *testing.T) {
	s := NewSplitter()
	k1 := s.Assign("u", ce(model.KindLifecycle, model.LcNavigate, "p1", "n1", 100))
	k2 := s.Assign("u", ce(model.KindMark, "a", "p1", "n1", 200))
	if k1 != k2 {
		t.Fatal("entries after navigate must share its unit")
	}
	// 应用内导航：NavID 变化
	k3 := s.Assign("u", ce(model.KindLifecycle, model.LcNavigate, "p1", "n2", 300))
	if k3 == k1 || k3.NavID != "n2" {
		t.Fatalf("in-app navigation must split unit, got %+v", k3)
	}
	// 跨页面导航
	k4 := s.Assign("u", ce(model.KindLifecycle, model.LcNavigate, "p2", "n3", 400))
	if k4.PageID != "p2" || k4 == k3 {
		t.Fatalf("page navigation must split unit, got %+v", k4)
	}
}

func TestSuspendResumeNotConcatenated(t *testing.T) {
	s := NewSplitter()
	s.Assign("u", ce(model.KindLifecycle, model.LcNavigate, "p1", "n1", 100))
	before := s.Assign("u", ce(model.KindMark, "a", "p1", "n1", 200))
	s.Assign("u", ce(model.KindLifecycle, model.LcSuspend, "p1", "n1", 300))
	resumeKey := s.Assign("u", ce(model.KindLifecycle, model.LcResume, "p1", "n1", 400))
	after := s.Assign("u", ce(model.KindMark, "b", "p1", "n1", 500))
	if before == after {
		t.Fatal("resume must start a new segment")
	}
	if resumeKey != after {
		t.Fatal("entries after resume belong to the new segment")
	}
	if after.Segment != before.Segment+1 {
		t.Fatalf("segment must increment, got %d -> %d", before.Segment, after.Segment)
	}
}

func TestBFCacheRestoreSplits(t *testing.T) {
	s := NewSplitter()
	s.Assign("u", ce(model.KindLifecycle, model.LcNavigate, "p1", "n1", 100))
	a := s.Assign("u", ce(model.KindFrame, "", "p1", "n1", 200))
	s.Assign("u", ce(model.KindLifecycle, model.LcSuspend, "p1", "n1", 300))
	b := s.Assign("u", ce(model.KindLifecycle, model.LcBFCacheRestore, "p1", "n1", 400))
	if a == b || b.Segment != a.Segment+1 {
		t.Fatalf("bfcache restore must split segments: %+v vs %+v", a, b)
	}
}

func TestImplicitNavigation(t *testing.T) {
	s := NewSplitter()
	k1 := s.Assign("u", ce(model.KindMark, "a", "p1", "n1", 100))
	k2 := s.Assign("u", ce(model.KindMark, "b", "p2", "n2", 200))
	if k1 == k2 {
		t.Fatal("page/nav change without navigate event must split")
	}
}
