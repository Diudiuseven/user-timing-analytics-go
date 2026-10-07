// Package longtask merges overlapping long tasks into de-duplicated blocked
// intervals and attributes blocked time to interaction/navigation windows.
//
// Overlapping (or duplicated) long tasks are interval-unioned first, so
// blocked time is counted at most once even when two entries cover the same
// period. Every merged interval is then cut against a set of attribution
// windows supplied by the caller: interaction entries (pointer/key/etc.)
// create [interactionStart, interactionEnd] windows; the navigation window
// covers the whole segment. Pieces not covered by any interaction window are
// attributed to the navigation id. Attribution shares are therefore exact,
// order independent, and sum to the unioned blocked duration.
package longtask

import "sort"

// Interval is a half-open time range in milliseconds.
type Interval struct {
	Start int64
	End   int64
}

// Window is an attribution window.
type Window struct {
	ID    string
	Start int64
	End   int64
}

// Stat is one unioned long-task interval with its per-window attribution.
type Stat struct {
	Start        int64
	End          int64
	Duration     float64
	Attributions []Share
}

// Share attributes a slice of blocked time to one window.
type Share struct {
	WindowID string
	Duration float64
}

// Merge unions overlapping/adjacent intervals (length > threshold only).
func Merge(iv []Interval, thresholdMs float64) []Interval {
	if len(iv) == 0 {
		return nil
	}
	sorted := append([]Interval(nil), iv...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	var out []Interval
	cur := sorted[0]
	for _, in := range sorted[1:] {
		if in.Start <= cur.End {
			if in.End > cur.End {
				cur.End = in.End
			}
			continue
		}
		if float64(cur.End-cur.Start) >= thresholdMs {
			out = append(out, cur)
		}
		cur = in
	}
	if float64(cur.End-cur.Start) >= thresholdMs {
		out = append(out, cur)
	}
	return out
}

// Attribute cuts merged intervals against windows. Uncovered pieces go to
// navID. If a point belongs to several interaction windows, each window gets
// its exact covering slice (interactions are expected not to overlap).
func Attribute(merged []Interval, windows []Window, navID string) []Stat {
	sortedWin := append([]Window(nil), windows...)
	sort.Slice(sortedWin, func(i, j int) bool {
		if sortedWin[i].Start != sortedWin[j].Start {
			return sortedWin[i].Start < sortedWin[j].Start
		}
		return sortedWin[i].End < sortedWin[j].End
	})

	stats := make([]Stat, 0, len(merged))
	for _, iv := range merged {
		st := Stat{Start: iv.Start, End: iv.End}
		type frag struct {
			id  string
			end int64
		}
		pos := iv.Start
		// Walk the windows that intersect this interval.
		for _, w := range sortedWin {
			if w.End <= iv.Start {
				continue
			}
			if w.Start >= iv.End {
				break
			}
			lo := max64(w.Start, iv.Start)
			hi := min64(w.End, iv.End)
			if lo > pos {
				st.Attributions = append(st.Attributions, Share{navID, float64(lo - pos)})
			}
			st.Attributions = append(st.Attributions, Share{w.ID, float64(hi - lo)})
			pos = hi
		}
		if pos < iv.End {
			st.Attributions = append(st.Attributions, Share{navID, float64(iv.End - pos)})
		}
		st.Duration = float64(iv.End - iv.Start)
		stats = append(stats, st)
	}
	return stats
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
