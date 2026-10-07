// Package metrics 计算自定义度量、长任务归因与帧率刻画。
//
// 自定义度量：measure 条目 Duration > 0 时直接采用；否则按名称配对
// "<name>:start" 与 "<name>:end" 标记计算时长。缺起点、缺终点、时长
// 异常（<=0 或超过上限）的度量标记为未完成并给出原因，不按零值参与统计。
//
// 长任务：重叠区间先合并再计时；按导航窗口（单元导航起点后的
// NavigationWindowMs）与交互窗口（名为 "interaction:*" 标记后的
// InteractionWindowMs）归因，导航优先。
//
// 帧率：除平均帧率外，刻画掉帧占比、卡顿帧数与最长停顿。
package metrics

import (
	"sort"
	"strings"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// Config 指标计算配置。
type Config struct {
	MaxMeasureDurationMs float64 // 度量时长上限，超过视为异常
	FrameBudgetMs        float64 // 基准帧间隔（如 16.7ms）
	JankThresholdMs      float64 // 卡顿帧阈值
	InteractionWindowMs  float64 // 交互归因窗口
	NavigationWindowMs   float64 // 导航归因窗口
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		MaxMeasureDurationMs: 60000,
		FrameBudgetMs:        16.7,
		JankThresholdMs:      50,
		InteractionWindowMs:  200,
		NavigationWindowMs:   3000,
	}
}

type measureRec struct {
	serverTime int64
	duration   float64 // >0 表示显式时长
}

type interval struct {
	start, end float64
}

// Aggregator 累积一个观测单元的指标。
type Aggregator struct {
	cfg Config

	marks    map[string][]int64 // 标记名 -> 有序服务端时间
	measures map[string][]measureRec
	longTask []interval
	frames   []float64

	navTime      int64 // 单元导航起点（首个 navigate 的服务端时间）
	hasNav       bool
	interactions []int64 // interaction:* 标记时间

	entryCount int
}

// NewAggregator 创建聚合器。
func NewAggregator(cfg Config) *Aggregator {
	return &Aggregator{
		cfg:      cfg,
		marks:    make(map[string][]int64),
		measures: make(map[string][]measureRec),
	}
}

// Add 吸收一条属于本单元的条目。同一单元的条目须按服务端时间有序送达。
func (a *Aggregator) Add(e model.Entry, serverTime int64) {
	a.entryCount++
	switch e.Kind {
	case model.KindMark:
		a.marks[e.Name] = append(a.marks[e.Name], serverTime)
		if strings.HasPrefix(e.Name, "interaction:") {
			a.interactions = append(a.interactions, serverTime)
		}
	case model.KindMeasure:
		a.measures[e.Name] = append(a.measures[e.Name], measureRec{serverTime: serverTime, duration: e.Duration})
	case model.KindLongTask:
		if e.Duration > 0 {
			a.longTask = append(a.longTask, interval{float64(serverTime), float64(serverTime) + e.Duration})
		}
	case model.KindFrame:
		if e.Duration > 0 {
			a.frames = append(a.frames, e.Duration)
		}
	case model.KindLifecycle:
		if e.Name == model.LcNavigate && !a.hasNav {
			a.navTime = serverTime
			a.hasNav = true
		}
	}
}

// Result 输出当前聚合结果。
func (a *Aggregator) Result(key model.UnitKey) *model.UnitMetrics {
	m := &model.UnitMetrics{
		Key:        key,
		Measures:   make(map[string]*model.MeasureAgg),
		Incomplete: make(map[string]int),
		EntryCount: a.entryCount,
	}
	a.computeMeasures(m)
	m.LongTasks = a.computeLongTasks()
	m.Frames = a.computeFrames()
	return m
}

// computeMeasures 解析全部度量；未完成度量计入 Incomplete 而不参与统计。
func (a *Aggregator) computeMeasures(m *model.UnitMetrics) {
	names := make([]string, 0, len(a.measures))
	for n := range a.measures {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		starts := a.marks[name+":start"]
		ends := a.marks[name+":end"]
		for _, rec := range a.measures[name] {
			d, ok, reason := a.resolveMeasure(rec, starts, ends)
			if !ok {
				m.Incomplete[string(reason)]++
				continue
			}
			agg := m.Measures[name]
			if agg == nil {
				agg = &model.MeasureAgg{Min: d, Max: d}
				m.Measures[name] = agg
			}
			agg.Count++
			agg.Sum += d
			if d < agg.Min {
				agg.Min = d
			}
			if d > agg.Max {
				agg.Max = d
			}
		}
	}
}

func (a *Aggregator) resolveMeasure(rec measureRec, starts, ends []int64) (float64, bool, model.IncompleteReason) {
	if rec.duration > 0 {
		if rec.duration > a.cfg.MaxMeasureDurationMs {
			return 0, false, model.ReasonAbnormalDuration
		}
		return rec.duration, true, ""
	}
	// 起点：不晚于度量记录时间的最后一个 start 标记。
	si := sort.Search(len(starts), func(i int) bool { return starts[i] > rec.serverTime }) - 1
	if si < 0 {
		return 0, false, model.ReasonMissingStart
	}
	start := starts[si]
	// 终点：起点之后的第一个 end 标记。
	ei := sort.Search(len(ends), func(i int) bool { return ends[i] >= start })
	if ei >= len(ends) {
		return 0, false, model.ReasonMissingEnd
	}
	d := float64(ends[ei] - start)
	if d <= 0 || d > a.cfg.MaxMeasureDurationMs {
		return 0, false, model.ReasonAbnormalDuration
	}
	return d, true, ""
}

// computeLongTasks 合并重叠区间后计时，并按导航/交互窗口归因。
func (a *Aggregator) computeLongTasks() model.LongTaskStats {
	st := model.LongTaskStats{TaskCount: len(a.longTask)}
	if len(a.longTask) == 0 {
		return st
	}
	sorted := append([]interval(nil), a.longTask...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	merged := make([]interval, 0, len(sorted))
	for _, iv := range sorted {
		if iv.end-iv.start > st.LongestMs {
			st.LongestMs = iv.end - iv.start
		}
		if n := len(merged); n > 0 && iv.start <= merged[n-1].end {
			if iv.end > merged[n-1].end {
				merged[n-1].end = iv.end
			}
			continue
		}
		merged = append(merged, iv)
	}
	// 归因窗口。
	var windows []struct {
		iv    interval
		isNav bool
	}
	if a.hasNav {
		windows = append(windows, struct {
			iv    interval
			isNav bool
		}{interval{float64(a.navTime), float64(a.navTime) + a.cfg.NavigationWindowMs}, true})
	}
	for _, t := range a.interactions {
		windows = append(windows, struct {
			iv    interval
			isNav bool
		}{interval{float64(t), float64(t) + a.cfg.InteractionWindowMs}, false})
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].isNav != windows[j].isNav {
			return windows[i].isNav // 导航优先
		}
		return windows[i].iv.start < windows[j].iv.start
	})
	for _, iv := range merged {
		total := iv.end - iv.start
		st.TotalBusyMs += total
		attributed := 0.0
		for _, w := range windows {
			ov := overlap(iv, w.iv)
			if ov <= 0 {
				continue
			}
			attributed += ov
			if w.isNav {
				st.ByNavigation += ov
			} else {
				st.ByInteraction += ov
			}
			// 从剩余区间中扣除已归因部分：简化为按窗口起点裁剪。
			iv = clipRemaining(iv, w.iv)
		}
		if rem := total - attributed; rem > 0 {
			st.Unattributed += rem
		}
	}
	return st
}

func overlap(a, b interval) float64 {
	s := a.start
	if b.start > s {
		s = b.start
	}
	e := a.end
	if b.end < e {
		e = b.end
	}
	if e <= s {
		return 0
	}
	return e - s
}

// clipRemaining 返回 iv 扣除 w 后的剩余部分（取较长的一段；
// 窗口相交情形下的近似，保证总量守恒由调用方用 attributed 保证）。
func clipRemaining(iv, w interval) interval {
	if w.end >= iv.start && w.start <= iv.start && w.end < iv.end {
		return interval{w.end, iv.end}
	}
	if w.start > iv.start && w.start < iv.end && w.end >= iv.end {
		return interval{iv.start, w.start}
	}
	if w.start <= iv.start && w.end >= iv.end {
		return interval{0, 0}
	}
	return iv
}

// computeFrames 帧率刻画：平均帧率、掉帧占比、卡顿帧数、最长停顿。
func (a *Aggregator) computeFrames() model.FrameStats {
	st := model.FrameStats{SampleCount: len(a.frames)}
	if len(a.frames) == 0 {
		return st
	}
	sum := 0.0
	dropped := 0
	dropThreshold := 1.5 * a.cfg.FrameBudgetMs
	for _, d := range a.frames {
		sum += d
		if d > dropThreshold {
			dropped++
		}
		if d > a.cfg.JankThresholdMs {
			st.JankCount++
		}
		if d > st.LongestStallMs {
			st.LongestStallMs = d
		}
	}
	mean := sum / float64(len(a.frames))
	if mean > 0 {
		st.AvgFPS = 1000 / mean
	}
	st.DroppedRatio = float64(dropped) / float64(len(a.frames))
	return st
}
