package engine

import (
	"encoding/json"

	"github.com/Diudiuseven/user-timing-analytics-go/aggregate"
)

// comparableWindow is the metric content of a window; bookkeeping fields
// (revision, computation timestamp, closed flag) are excluded so that a
// recompute with identical input compares equal.
type comparableWindow struct {
	Key         aggregate.WindowResult `json:"-"`
	WindowStart int64
	WindowEnd   int64
	Measures    map[string]*aggregate.MeasureAgg
	LongTasks   aggregate.LongTaskAgg
	Frames      interface{}
}

func canonical(w *aggregate.WindowResult) string {
	c := struct {
		WindowStart int64
		WindowEnd   int64
		Measures    map[string]*aggregate.MeasureAgg
		LongTasks   aggregate.LongTaskAgg
		Frames      interface{}
	}{w.WindowStart, w.WindowEnd, w.Measures, w.LongTasks, w.Frames}
	// Sort maps via JSON marshal (Go marshals maps with sorted keys).
	data, _ := json.Marshal(c)
	return string(data)
}

func equalWindow(a, b *aggregate.WindowResult) bool {
	if a == nil || b == nil {
		return a == b
	}
	return canonical(a) == canonical(b)
}
