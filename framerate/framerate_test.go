package framerate

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func TestAnalyze(t *testing.T) {
	cfg := model.Config{FrameBudgetMs: 1000.0 / 60.0, DroppedFpsThreshold: 50, JankFpsThreshold: 30, JankRunLength: 3, StallGapMs: 1000}
	samples := []Sample{
		{TimeMs: 0, Fps: 60}, {TimeMs: 16, Fps: 60},
		{TimeMs: 32, Fps: 20}, {TimeMs: 48, Fps: 15}, {TimeMs: 64, Fps: 10}, // jank run
		{TimeMs: 80, Fps: 60},
		{TimeMs: 2000, Fps: 12}, {TimeMs: 2016, Fps: 12}, // short run, not jank
	}
	st := Analyze(samples, cfg)
	if st.SampleCount != 8 || st.AvgFps <= 0 || st.MinFps != 10 {
		t.Fatalf("basic stats wrong: %+v", st)
	}
	if st.DroppedFrames != 5 {
		t.Fatalf("dropped = %d, want 5", st.DroppedFrames)
	}
	if st.JankEvents != 1 {
		t.Fatalf("jank events = %d, want exactly one long stall", st.JankEvents)
	}
	if st.LongestStallMs <= 0 {
		t.Fatal("longest stall must be recorded")
	}
}

func TestDeterministicAcrossOrder(t *testing.T) {
	cfg := model.Config{DroppedFpsThreshold: 50, JankFpsThreshold: 30, JankRunLength: 3, StallGapMs: 1000, FrameBudgetMs: 16.7}
	a := Analyze([]Sample{{Key: "a", TimeMs: 0, Fps: 60}, {Key: "b", TimeMs: 16, Fps: 20}, {Key: "c", TimeMs: 32, Fps: 10}}, cfg)
	b := Analyze([]Sample{{Key: "c", TimeMs: 32, Fps: 10}, {Key: "a", TimeMs: 0, Fps: 60}, {Key: "b", TimeMs: 16, Fps: 20}}, cfg)
	if a != b {
		t.Fatalf("out-of-order samples must converge: %+v vs %+v", a, b)
	}
}
