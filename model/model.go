// Package model 定义上报数据与指标结果的核心类型。
package model

// EntryKind 条目类型。
type EntryKind string

const (
	KindMark      EntryKind = "mark"      // 起止标记
	KindMeasure   EntryKind = "measure"   // 度量记录（可显式给出时长）
	KindLongTask  EntryKind = "longtask"  // 长任务记录
	KindFrame     EntryKind = "frame"     // 帧率采样
	KindLifecycle EntryKind = "lifecycle" // 页面生命周期事件
)

// Lifecycle 事件名。
const (
	LcNavigate       = "navigate"        // 导航（含应用内导航）
	LcSuspend        = "suspend"         // 页面被挂起
	LcResume         = "resume"          // 从挂起恢复
	LcBFCacheRestore = "bfcache_restore" // 前进后退缓存恢复
	LcUnload         = "unload"          // 页面卸载
)

// Entry 是一条上报条目。ClientTime 为客户端时钟毫秒时间戳。
type Entry struct {
	ID         string    `json:"id"` // 客户端生成的稳定唯一 ID（去重键）
	Kind       EntryKind `json:"kind"`
	Name       string    `json:"name"`        // mark/measure 名称，lifecycle 事件名
	ClientTime int64     `json:"client_time"` // 事件发生时的客户端时钟（ms）
	Duration   float64   `json:"duration"`    // measure/longtask 时长（ms）；frame 为帧间隔
	PageID     string    `json:"page_id"`     // 页面标识
	NavID      string    `json:"nav_id"`      // 导航标识（应用内导航时变化）
	Meta       string    `json:"meta"`        // 附加信息（如长任务归因的交互 ID）
}

// Batch 是一批上报数据。
type Batch struct {
	BatchID          string  `json:"batch_id"`
	UserID           string  `json:"user_id"`
	Entries          []Entry `json:"entries"`
	ClientSentAt     int64   `json:"client_sent_at"` // 客户端发送时刻（客户端时钟 ms）
	ServerReceivedAt int64   `json:"-"`              // 服务端接收时刻（服务端时钟 ms），由服务端填充
}

// UnitKey 标识一个观测单元：同一用户、同一页面、同一次导航、同一段生命周期。
type UnitKey struct {
	UserID  string `json:"user_id"`
	PageID  string `json:"page_id"`
	NavID   string `json:"nav_id"`
	Segment int    `json:"segment"`
}

// IncompleteReason 度量未完成原因。
type IncompleteReason string

const (
	ReasonMissingStart     IncompleteReason = "missing_start"
	ReasonMissingEnd       IncompleteReason = "missing_end"
	ReasonAbnormalDuration IncompleteReason = "abnormal_duration"
)

// MeasureResult 单个自定义度量的计算结果。
type MeasureResult struct {
	Name      string           `json:"name"`
	Duration  float64          `json:"duration"` // 仅 Completed 时有效
	Completed bool             `json:"completed"`
	Reason    IncompleteReason `json:"reason,omitempty"`
}

// LongTaskStats 长任务统计（重叠区间不重复计时）。
type LongTaskStats struct {
	TotalBusyMs   float64 `json:"total_busy_ms"`  // 区间并集总时长
	TaskCount     int     `json:"task_count"`     // 原始任务条数
	LongestMs     float64 `json:"longest_ms"`     // 最长单任务
	ByInteraction float64 `json:"by_interaction"` // 归因到交互窗口的忙碌时长
	ByNavigation  float64 `json:"by_navigation"`  // 归因到导航窗口的忙碌时长
	Unattributed  float64 `json:"unattributed"`   // 无法归因的忙碌时长
}

// FrameStats 帧率刻画。
type FrameStats struct {
	SampleCount    int     `json:"sample_count"`
	AvgFPS         float64 `json:"avg_fps"`
	DroppedRatio   float64 `json:"dropped_ratio"`    // 掉帧占比（帧间隔 > 1.5 倍基准）
	JankCount      int     `json:"jank_count"`       // 卡顿帧数（帧间隔超过 JankThresholdMs）
	LongestStallMs float64 `json:"longest_stall_ms"` // 最长单帧停顿
}

// UnitMetrics 一个观测单元的完整指标结果。
type UnitMetrics struct {
	Key        UnitKey                `json:"key"`
	Measures   map[string]*MeasureAgg `json:"measures"`
	Incomplete map[string]int         `json:"incomplete"` // reason -> count
	LongTasks  LongTaskStats          `json:"long_tasks"`
	Frames     FrameStats             `json:"frames"`
	EntryCount int                    `json:"entry_count"`
}

// MeasureAgg 同名度量的聚合。
type MeasureAgg struct {
	Count int     `json:"count"`
	Sum   float64 `json:"sum"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}
