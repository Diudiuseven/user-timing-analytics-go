package metrics

import (
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func key() model.UnitKey { return model.UnitKey{UserID: "u", PageID: "p", NavID: "n", Segment: 0} }

func TestMeasureFromMarks(t *testing.T) {
	a := NewAggregator(DefaultConfig())
	a.Add(model.Entry{Kind: model.KindMark, Name: "load:start"}, 1000)
	a.Add(model.Entry{Kind: model.KindMark, Name: "load:end"}, 1600)
	a.Add(model.Entry{Kind: model.KindMeasure, Name: "load"}, 1700)
	m := a.Result(key())
	agg := m.Measures["load"]
	if agg == nil || agg.Count != 1 || agg.Sum != 600 {
		t.Fatalf("want 600ms measure, got %+v", agg)
	}
	if len(m.Incomplete) != 0 {
		t.Fatalf("no incomplete expected, got %v", m.Incomplete)
	}
}

func TestMeasureIncompleteReasons(t *testing.T) {
	a := NewAggregator(DefaultConfig())
	// 缺起点
	a.Add(model.Entry{Kind: model.KindMark, Name: "m1:end"}, 1000)
	a.Add(model.Entry{Kind: model.KindMeasure, Name: "m1"}, 1100)
	// 缺终点
	a.Add(model.Entry{Kind: model.KindMark, Name: "m2:start"}, 1000)
	a.Add(model.Entry{Kind: model.KindMeasure, Name: "m2"}, 1100)
	// 时长异常（end 与 start 同刻，计算出 0）
	a.Add(model.Entry{Kind: model.KindMark, Name: "m3:start"}, 1000)
	a.Add(model.Entry{Kind: model.KindMark, Name: "m3:end"}, 1000)
	a.Add(model.Entry{Kind: model.KindMeasure, Name: "m3"}, 1100)
	// 显式时长超限
	a.Add(model.Entry{Kind: model.KindMeasure, Name: "m4", Duration: 1e9}, 1200)
	m := a.Result(key())
	if m.Incomplete[string(model.ReasonMissingStart)] != 1 {
		t.Fatalf("missing_start: %v", m.Incomplete)
	}
	if m.Incomplete[string(model.ReasonMissingEnd)] != 1 {
		t.Fatalf("missing_end: %v", m.Incomplete)
	}
	if m.Incomplete[string(model.ReasonAbnormalDuration)] != 2 {
		t.Fatalf("abnormal_duration: %v", m.Incomplete)
	}
	if len(m.Measures) != 0 {
		t.Fatalf("incomplete measures must not be counted: %v", m.Measures)
	}
}

func TestLongTaskOverlapMerging(t *testing.T) {
	a := NewAggregator(DefaultConfig())
	// [100,200) 与 [150,300) 重叠，并集 200ms，不得计 250ms
	a.Add(model.Entry{Kind: model.KindLongTask, Duration: 100}, 100)
	a.Add(model.Entry{Kind: model.KindLongTask, Duration: 150}, 150)
	m := a.Result(key())
	if m.LongTasks.TotalBusyMs != 200 {
		t.Fatalf("want 200, got %v", m.LongTasks.TotalBusyMs)
	}
	if m.LongTasks.TaskCount != 2 || m.LongTasks.LongestMs != 150 {
		t.Fatalf("bad stats: %+v", m.LongTasks)
	}
}

func TestLongTaskAttribution(t *testing.T) {
	cfg := DefaultConfig()
	a := NewAggregator(cfg)
	a.Add(model.Entry{Kind: model.KindLifecycle, Name: model.LcNavigate}, 0)
	// 导航窗口 [0,3000) 内的长任务
	a.Add(model.Entry{Kind: model.KindLongTask, Duration: 100}, 500)
	// 交互窗口 [5000,5200) 内的长任务
	a.Add(model.Entry{Kind: model.KindMark, Name: "interaction:click"}, 5000)
	a.Add(model.Entry{Kind: model.KindLongTask, Duration: 80}, 5050)
	// 窗口之外
	a.Add(model.Entry{Kind: model.KindLongTask, Duration: 60}, 9000)
	m := a.Result(key())
	lt := m.LongTasks
	if lt.ByNavigation != 100 || lt.ByInteraction != 80 || lt.Unattributed != 60 {
		t.Fatalf("bad attribution: %+v", lt)
	}
}

func TestFrameStats(t *testing.T) {
	a := NewAggregator(DefaultConfig())
	// 8 帧正常 16.7ms，1 帧掉帧 30ms，1 帧卡顿 120ms
	for i := 0; i < 8; i++ {
		a.Add(model.Entry{Kind: model.KindFrame, Duration: 16.7}, int64(i*17))
	}
	a.Add(model.Entry{Kind: model.KindFrame, Duration: 30}, 1000)
	a.Add(model.Entry{Kind: model.KindFrame, Duration: 120}, 2000)
	m := a.Result(key())
	f := m.Frames
	if f.SampleCount != 10 {
		t.Fatalf("count: %d", f.SampleCount)
	}
	if f.DroppedRatio != 0.2 {
		t.Fatalf("dropped ratio: %v", f.DroppedRatio)
	}
	if f.JankCount != 1 || f.LongestStallMs != 120 {
		t.Fatalf("jank: %+v", f)
	}
	if f.AvgFPS >= 50 {
		t.Fatalf("avg fps should reflect stalls (well below 60): %v", f.AvgFPS)
	}
}
