// Package aggregate assembles deterministic window results from a
// navigation segment. It combines custom-measure resolution, long-task
// merge/attribution and frame-rate analysis into one WindowResult.
//
// Every sketch used here is a mergeable monoid (numeric sketches keep all
// complete samples and derive exact quantiles), so the engine can update a
// window incrementally by recomputing only it and still produce results
// identical to a full recomputation. Incomplete measures are never folded
// into the duration sketch: they are counted separately by reason instead.
package aggregate

import (
	"sort"

	"github.com/Diudiuseven/user-timing-analytics-go/framerate"
	"github.com/Diudiuseven/user-timing-analytics-go/longtask"
	"github.com/Diudiuseven/user-timing-analytics-go/measure"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/session"
)

// NumericStats is an exact, mergeable numeric sketch.
type NumericStats struct {
	Count int
	Sum   float64
	Min   float64
	Max   float64
	Mean  float64
	P50   float64
	P95   float64
	P99   float64
	vals  []float64
}

// Add folds one value in.
func (n *NumericStats) Add(v float64) {
	if n.Count == 0 || v < n.Min {
		n.Min = v
	}
	if n.Count == 0 || v > n.Max {
		n.Max = v
	}
	n.Sum += v
	n.Count++
	n.Mean = n.Sum / float64(n.Count)
	n.vals = append(n.vals, v)
}

// Merge folds another sketch in (must be finalized or freshly built from the
// same value stream; values order is fixed before quantile reads).
func (n *NumericStats) Merge(o *NumericStats) {
	n.vals = append(n.vals, o.vals...)
	n.Count += o.Count
	n.Sum += o.Sum
	if o.Count > 0 {
		if n.Count == o.Count || o.Min < n.Min {
			n.Min = o.Min
		}
		if o.Max > n.Max {
			n.Max = o.Max
		}
	}
	if n.Count > 0 {
		n.Mean = n.Sum / float64(n.Count)
	}
}

// Finalize computes exact quantiles.
func (n *NumericStats) Finalize() {
	sort.Float64s(n.vals)
	n.P50 = pct(n.vals, 50)
	n.P95 = pct(n.vals, 95)
	n.P99 = pct(n.vals, 99)
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	r := (p / 100) * float64(len(sorted)-1)
	lo := int(r)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(r-float64(lo))
}

// MeasureAgg aggregates one custom measure name.
type MeasureAgg struct {
	Complete        NumericStats
	Incomplete      map[model.SegStatus]int
	IncompleteTotal int
}

// LongTaskAgg aggregates unioned long tasks.
type LongTaskAgg struct {
	TaskCount      int
	TotalBlockedMs float64
	MaxBlockedMs   float64
	ByWindow       map[string]float64
}

// WindowResult is the computed result for one observation window.
type WindowResult struct {
	Key          model.WindowKey
	WindowStart  int64
	WindowEnd    int64
	Closed       bool
	Measures     map[string]*MeasureAgg
	LongTasks    LongTaskAgg
	Frames       model.FrameStat
	Revision     int64
	ComputedAtMs int64
}

// Build computes a window from one navigation segment.
func Build(key model.WindowKey, seg session.NavSegment, cfg model.Config, nowMs int64) *WindowResult {
	w := &WindowResult{Key: key, WindowStart: seg.StartMs, WindowEnd: seg.EndMs, Measures: map[string]*MeasureAgg{}}

	// Custom measures.
	for _, r := range measure.Resolve(seg.Events, cfg) {
		agg := w.Measures[r.Name]
		if agg == nil {
			agg = &MeasureAgg{Incomplete: map[model.SegStatus]int{}}
			w.Measures[r.Name] = agg
		}
		if r.Status == model.SegComplete {
			agg.Complete.Add(r.DurMs)
		} else {
			agg.Incomplete[r.Status]++
			agg.IncompleteTotal++
		}
	}
	for _, a := range w.Measures {
		a.Complete.Finalize()
	}

	// Long tasks: union, then attribute to interaction windows.
	var ivs []longtask.Interval
	var wins []longtask.Window
	threshold := cfg.LongTaskThresholdMs
	if threshold <= 0 {
		threshold = 50
	}
	for _, e := range seg.Events {
		if e.Kind == model.KindLongTask && float64(e.End-e.Start) >= threshold {
			ivs = append(ivs, longtask.Interval{Start: e.Start, End: e.End})
		}
		if e.Kind == model.KindInteraction {
			wins = append(wins, longtask.Window{
				ID: interactionID(e), Start: e.Start, End: max64(e.End, e.Start+1),
			})
		}
	}
	merged := longtask.Merge(ivs, threshold)
	w.LongTasks.ByWindow = map[string]float64{}
	stats := longtask.Attribute(merged, wins, "nav:"+seg.NavID)
	for _, st := range stats {
		w.LongTasks.TaskCount++
		w.LongTasks.TotalBlockedMs += st.Duration
		if st.Duration > w.LongTasks.MaxBlockedMs {
			w.LongTasks.MaxBlockedMs = st.Duration
		}
		for _, a := range st.Attributions {
			w.LongTasks.ByWindow[a.WindowID] += a.Duration
		}
	}

	// Frames.
	w.Frames = framerate.Analyze(framerate.FromEvents(seg.Events), cfg)
	w.ComputedAtMs = nowMs
	return w
}

func interactionID(e model.Event) string {
	if e.Source != "" {
		return "ixn:" + e.Source
	}
	return "ixn:" + e.Key
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Tumble splits a segment's events into fixed-width tumbling sub-segments
// aligned to an epoch boundary of tumbleMs. Events are assigned by their
// start timestamp; a zero/negative tumbleMs yields no sub-segments. The
// split is deterministic and never stitches a suspend gap because input is
// already a single post-reconstruction segment.
func Tumble(seg session.NavSegment, tumbleMs int64) []session.NavSegment {
	if tumbleMs <= 0 {
		return nil
	}
	buckets := map[int64]*session.NavSegment{}
	var order []int64
	for _, ev := range seg.Events {
		idx := floorDiv(ev.Start, tumbleMs)
		b := buckets[idx]
		if b == nil {
			b = &session.NavSegment{NavID: seg.NavID, Segment: seg.Segment, TumbleIdx: idx,
				StartMs: idx * tumbleMs, EndMs: idx*tumbleMs + tumbleMs}
			buckets[idx] = b
			order = append(order, idx)
		}
		b.Events = append(b.Events, ev)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]session.NavSegment, 0, len(order))
	for _, idx := range order {
		out = append(out, *buckets[idx])
	}
	return out
}

func floorDiv(a, b int64) int64 {
	q := a / b
	// Go truncates toward zero; for a negative timestamp floor one down so
	// buckets stay epoch-aligned on both sides of zero.
	if r := a % b; r != 0 && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
