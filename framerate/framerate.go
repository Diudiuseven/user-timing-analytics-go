// Package framerate characterizes frame-rate samples beyond a mean.
//
// From per-frame samples (timestamp + instantaneous fps, optionally an
// explicit frame duration in milliseconds) it reports:
//
//   - average / minimum / 5th-percentile fps (the distribution's low tail);
//   - dropped-frame count: frames slower than DroppedFpsThreshold (or whose
//     duration exceeds FrameBudgetMs when explicit durations are present);
//   - jank events: runs of at least JankRunLength consecutive frames slower
//     than JankFpsThreshold, split apart when the gap between frames is
//     larger than StallGapMs (which means a separate stall episode);
//   - longest stall: wall-clock span (ms) covered by the longest jank run.
//
// The computation is a deterministic fold over samples sorted by timestamp,
// which keeps incremental and full recomputation identical.
package framerate

import (
	"sort"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// Sample is one frame observation.
type Sample struct {
	TimeMs int64
	Fps    float64
	DurMs  float64
	Key    string
}

// FromEvents extracts frame samples from normalized events.
func FromEvents(events []model.Event) []Sample {
	var out []Sample
	for _, e := range events {
		if e.Kind != model.KindFrame {
			continue
		}
		out = append(out, Sample{TimeMs: e.Start, Fps: e.Fps, DurMs: float64(e.End - e.Start), Key: e.Key})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TimeMs != out[j].TimeMs {
			return out[i].TimeMs < out[j].TimeMs
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Analyze computes frame statistics.
func Analyze(samples []Sample, cfg model.Config) model.FrameStat {
	var st model.FrameStat
	if len(samples) == 0 {
		return st
	}
	st.SampleCount = len(samples)
	fps := make([]float64, len(samples))
	minFps := samples[0].Fps
	sumFps := 0.0
	for i, s := range samples {
		fps[i] = s.Fps
		sumFps += s.Fps
		if s.Fps < minFps {
			minFps = s.Fps
		}
		dropped := false
		if cfg.DroppedFpsThreshold > 0 && s.Fps < cfg.DroppedFpsThreshold {
			dropped = true
		}
		if s.DurMs > 0 && cfg.FrameBudgetMs > 0 && s.DurMs > cfg.FrameBudgetMs {
			dropped = true
		}
		if dropped {
			st.DroppedFrames++
		}
	}
	sort.Float64s(fps)
	st.AvgFps = sumFps / float64(len(samples))
	st.MinFps = minFps
	st.P5Fps = percentileSorted(fps, 5)

	// Jank runs.
	run := 0
	var runStart, runLast int64
	threshold := cfg.JankFpsThreshold
	if threshold <= 0 {
		threshold = cfg.DroppedFpsThreshold
	}
	finishRun := func(lastTime int64) {
		if run >= cfg.JankRunLength {
			st.JankEvents++
			budget := cfg.FrameBudgetMs
			if budget <= 0 {
				budget = 1000.0 / 60.0
			}
			span := float64(lastTime-runStart) + budget
			if span > st.LongestStallMs {
				st.LongestStallMs = span
			}
		}
		run = 0
	}
	for _, s := range samples {
		slow := threshold > 0 && s.Fps < threshold
		if !slow {
			if run > 0 {
				finishRun(runLast)
			}
			continue
		}
		if run > 0 && cfg.StallGapMs > 0 && s.TimeMs-runLast > cfg.StallGapMs {
			finishRun(runLast)
		}
		if run == 0 {
			runStart = s.TimeMs
		}
		runLast = s.TimeMs
		run++
	}
	if run > 0 {
		finishRun(runLast)
	}
	return st
}

func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac
}
