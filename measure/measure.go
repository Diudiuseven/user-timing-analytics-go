// Package measure resolves custom User Timing measures.
//
// A measure record (model.KindMeasure) names a start mark and an end mark
// (encoded in Detail as "startMark,endMark"; an empty Detail means the
// record carries no marks at all -> SegOrphan). Resolution happens inside a
// single navigation segment: marks never match across navigation or
// suspension boundaries.
//
// Outcome classification is mutually exclusive and explicit:
//
//   - complete:        both marks found, end >= start, duration within
//     configured sane bounds;
//   - missing_start:   no mark with the start name exists in the segment;
//   - missing_end:     no mark with the end name exists at/after the start;
//   - bad_duration:    marks found but duration is negative/zero or outside
//     [MeasureMinDurMs, MeasureMaxDurMs];
//   - orphan_record:   the measure record named no marks at all.
//
// Incomplete measures are NEVER counted as zero: aggregation keeps only
// complete durations in the numeric sketch and counts incomplete outcomes by
// reason. Tie-breaking on duplicate marks is deterministic (stable key
// order), so replayed/out-of-order input yields identical results.
package measure

import (
	"sort"
	"strings"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// Resolve evaluates every measure record in one segment.
func Resolve(events []model.Event, cfg model.Config) []model.MeasureResult {
	marks := make(map[string][]model.Event)
	var measures []model.Event
	for _, e := range events {
		switch e.Kind {
		case model.KindMark:
			if e.Name != "" {
				marks[e.Name] = append(marks[e.Name], e)
			}
		case model.KindMeasure:
			measures = append(measures, e)
		}
	}
	for _, list := range marks {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Start != list[j].Start {
				return list[i].Start < list[j].Start
			}
			return list[i].Key < list[j].Key
		})
	}
	sort.Slice(measures, func(i, j int) bool {
		if measures[i].Start != measures[j].Start {
			return measures[i].Start < measures[j].Start
		}
		return measures[i].Key < measures[j].Key
	})

	out := make([]model.MeasureResult, 0, len(measures))
	for _, m := range measures {
		startName, endName, hasMarks := markPair(m.Detail)
		r := model.MeasureResult{Name: m.Name}
		if !hasMarks {
			r.Status = model.SegOrphan
			r.Reason = "measure record carries no start/end mark names"
			out = append(out, r)
			continue
		}
		starts, sOK := marks[startName]
		if !sOK {
			r.Status = model.SegNoStart
			r.Reason = "start mark not found in segment: " + startName
			out = append(out, r)
			continue
		}
		startEv := starts[len(starts)-1] // most recent deterministic start
		r.Start = startEv.Start

		ends, eOK := marks[endName]
		var endEv model.Event
		found := false
		if eOK {
			idx := sort.Search(len(ends), func(i int) bool {
				if ends[i].Start != startEv.Start {
					return ends[i].Start >= startEv.Start
				}
				return ends[i].Key >= startEv.Key
			})
			if idx < len(ends) {
				endEv = ends[idx]
				found = true
			}
		}
		if !found {
			if eOK {
				// The end mark exists but lies before the start mark: that is
				// an inverted (non-positive) duration, not a missing mark.
				r.End = ends[0].Start
				r.Status = model.SegBadDur
				r.Reason = "end mark precedes start mark"
				out = append(out, r)
				continue
			}
			r.Status = model.SegNoEnd
			r.Reason = "end mark not found in segment at/after start: " + endName
			out = append(out, r)
			continue
		}
		r.End = endEv.Start
		r.DurMs = float64(endEv.Start-startEv.Start) + subMs(endEv) - subMs(startEv)

		if r.DurMs <= 0 ||
			(cfg.MeasureMinDurMs > 0 && r.DurMs < cfg.MeasureMinDurMs) ||
			(cfg.MeasureMaxDurMs > 0 && r.DurMs > cfg.MeasureMaxDurMs) {
			r.Status = model.SegBadDur
			r.Reason = "duration outside sane bounds"
			r.DurMs = 0 // never let an abnormal duration leak into statistics
			out = append(out, r)
			continue
		}
		r.Status = model.SegComplete
		out = append(out, r)
	}
	return out
}

// markPair parses "startMark,endMark". hasMarks is false for an empty spec.
func markPair(detail string) (start, end string, hasMarks bool) {
	if detail == "" {
		return "", "", false
	}
	if i := strings.IndexByte(detail, ','); i >= 0 {
		return detail[:i], detail[i+1:], true
	}
	return detail, "", true
}

// subMs returns the intra-millisecond fraction of an event end (0 here:
// timestamps are integral ms); kept for clarity of the duration formula.
func subMs(model.Event) float64 { return 0 }
