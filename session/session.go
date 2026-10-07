// Package session reconstructs the observation timeline of one
// (user, session, page) triple from normalized events and splits it into
// navigations and segments.
//
// Navigation boundaries (both cut a new navigation):
//   - an event whose batch advertises a NavID different from the current
//     navigation's id (full page load / route change known to the client);
//   - a mark named NavMarkName ("app-navigation", detail may carry an
//     explicit navigation id), representing SPA in-app navigation.
//
// Segment boundaries WITHIN a navigation keep pre-suspend and post-resume
// metrics apart:
//   - "freeze" and "pagehide" suspend the current segment; the suspend event
//     itself stays in the old segment;
//   - "resume", "pageshow" with persisted=true and "bfcache_restore" open a
//     fresh segment before the event is assigned;
//   - if ordinary events keep flowing after a suspend without an explicit
//     resume (the client dropped it), the first such event implicitly opens
//     a fresh segment too.
//
// Segments are the atomic observation units: metrics are never stitched
// across a suspension. The reconstruction is a pure function of the event
// set, so out-of-order and late arrival converge to the same timeline.
package session

import (
	"sort"
	"strconv"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// NavMarkName is the mark name that denotes an in-app navigation boundary.
const NavMarkName = "app-navigation"

// NavSegment is one navigation segment (the atomic observation window).
type NavSegment struct {
	NavID     string
	Segment   int
	TumbleIdx int64 // epoch-aligned tumble bucket; -1 for the segment-level window
	Events    []model.Event
	StartMs   int64
	EndMs     int64
}

// Timeline holds all segments of one (user, session, page).
type Timeline struct {
	UserID    string
	SessionID string
	PageID    string
	Segments  []NavSegment
}

type segAcc struct {
	navID     string
	segment   int
	suspended bool
	events    []model.Event
}

// Build reconstructs the timeline. batchNav maps batchID -> advertised
// navID (may be nil).
func Build(userID, sessionID, pageID string, events []model.Event, batchNav map[string]string) *Timeline {
	ordered := append([]model.Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].Key < ordered[j].Key
	})

	tl := &Timeline{UserID: userID, SessionID: sessionID, PageID: pageID}

	var cur *segAcc
	navCount := 0

	emit := func(s *segAcc) {
		if s == nil || len(s.events) == 0 {
			return
		}
		out := NavSegment{NavID: s.navID, Segment: s.segment, TumbleIdx: -1, Events: s.events}
		out.StartMs = s.events[0].Start
		out.EndMs = s.events[0].End
		for _, e := range s.events[1:] {
			if e.Start < out.StartMs {
				out.StartMs = e.Start
			}
			if e.End > out.EndMs {
				out.EndMs = e.End
			}
		}
		tl.Segments = append(tl.Segments, out)
	}
	// closeSeg finalizes the current segment and opens a fresh one in the
	// same navigation (a suspend/resume boundary).
	closeSeg := func() {
		if cur == nil {
			return
		}
		emit(cur)
		cur = &segAcc{navID: cur.navID, segment: cur.segment + 1}
	}
	// switchNav finalizes segment and navigation, then opens a new nav seg0.
	switchNav := func(id string) {
		if cur != nil {
			emit(cur)
		}
		cur = &segAcc{navID: id}
	}
	ensureNav := func(advertised string) {
		if cur != nil {
			return
		}
		navCount++
		id := advertised
		if id == "" {
			id = "nav:" + strconv.Itoa(navCount)
		}
		switchNav(id)
	}

	for _, e := range ordered {
		advertised := batchNav[e.BatchID]

		// 1) Navigation switching.
		if e.Kind == model.KindMark && e.Name == NavMarkName {
			id := e.Detail
			if id == "" {
				id = advertised
			}
			if id == "" || (cur != nil && id == cur.navID) {
				navCount++
				id = "nav:syn:" + strconv.Itoa(navCount)
			}
			switchNav(id)
		} else if advertised != "" && (cur == nil || advertised != cur.navID) {
			switchNav(advertised)
		} else {
			ensureNav(advertised)
		}

		// 2) Segment switching around suspension.
		resume := e.Kind == model.KindLifecycle &&
			(e.Life == model.LifeResume || e.Life == model.LifeBFCacheRestore ||
				(e.Life == model.LifePageShow && e.Persisted))
		suspend := e.Kind == model.KindLifecycle &&
			(e.Life == model.LifeFreeze || e.Life == model.LifePageHide)
		if resume {
			closeSeg()
		} else if !suspend && cur.suspended {
			// Implicit resume: events after a suspend with no explicit resume.
			closeSeg()
		}

		cur.events = append(cur.events, e)
		if suspend {
			cur.suspended = true
		}
	}
	if cur != nil {
		emit(cur)
	}
	return tl
}
