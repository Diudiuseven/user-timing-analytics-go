// Package model defines the wire and domain contracts of the user-timing
// analytics engine: raw reported entries, normalized timeline events,
// computed metrics and engine configuration.
package model

import "time"

// EntryKind enumerates the kinds of entries a reporting client can send.
type EntryKind string

const (
	KindMark        EntryKind = "mark"
	KindMeasure     EntryKind = "measure"
	KindLongTask    EntryKind = "longtask"
	KindFrame       EntryKind = "frame"
	KindLifecycle   EntryKind = "lifecycle"
	KindInteraction EntryKind = "interaction"
)

// LifecycleType enumerates page-lifecycle events.
type LifecycleType string

const (
	LifePageShow       LifecycleType = "pageshow"
	LifePageHide       LifecycleType = "pagehide"
	LifeFreeze         LifecycleType = "freeze"
	LifeResume         LifecycleType = "resume"
	LifeBFCacheRestore LifecycleType = "bfcache_restore"
)

// Entry is one raw reported item. All timestamps are client wall-clock
// milliseconds (unix epoch ms). StartTime/Duration follow the semantics of
// the W3C PerformanceEntry APIs: startMs is the entry start epoch ms and
// durMs its duration in milliseconds.
type Entry struct {
	Kind      EntryKind     `json:"kind"`
	Name      string        `json:"name,omitempty"`
	StartMs   int64         `json:"startMs"`
	DurMs     float64       `json:"durMs,omitempty"`
	Life      LifecycleType `json:"life,omitempty"`
	Persisted bool          `json:"persisted,omitempty"` // pageshow from bfcache
	Fps       float64       `json:"fps,omitempty"`       // frame: instantaneous fps
	Source    string        `json:"source,omitempty"`    // interaction source id
	Detail    string        `json:"detail,omitempty"`
}

// Batch is one upload from a client. ServerTimeMs is the server receive time
// (unix epoch ms), used for clock-offset estimation.
type Batch struct {
	BatchID      string  `json:"batchId"`
	UserID       string  `json:"userId"`
	SessionID    string  `json:"sessionId"`
	PageID       string  `json:"pageId"`
	NavID        string  `json:"navId,omitempty"`
	Seq          int64   `json:"seq,omitempty"`
	ServerTimeMs int64   `json:"serverTimeMs,omitempty"`
	Entries      []Entry `json:"entries"`
}

// Event is a normalized, de-duplicated timeline element. All timestamps are
// corrected server-axis epoch ms; start is inclusive, end is start+duration
// (end == start for zero-duration points).
type Event struct {
	Key       string
	Kind      EntryKind
	Name      string
	Start     int64
	End       int64
	Life      LifecycleType
	Persisted bool
	Fps       float64
	Source    string
	Detail    string
	BatchID   string
}

// SegStatus classifies a custom measure outcome.
type SegStatus string

const (
	SegComplete SegStatus = "complete"
	SegNoStart  SegStatus = "missing_start"
	SegNoEnd    SegStatus = "missing_end"
	SegBadDur   SegStatus = "bad_duration"
	SegOrphan   SegStatus = "orphan_record"
)

// MeasureResult is the resolved outcome of one measure record.
type MeasureResult struct {
	Name   string
	Status SegStatus
	Start  int64
	End    int64
	DurMs  float64
	Reason string
}

// FrameStat summarizes frame-rate samples over a window.
type FrameStat struct {
	SampleCount    int
	AvgFps         float64
	MinFps         float64
	P5Fps          float64
	DroppedFrames  int
	JankEvents     int
	LongestStallMs float64
}

// WindowKey identifies a window deterministically.
type WindowKey struct {
	UserID    string
	SessionID string
	PageID    string
	NavID     string
	Segment   int
	TumbleIdx int64 // -1 when tumbling windows are disabled
}

// Revision records that an already-published window was recomputed.
type Revision struct {
	Window       WindowKey
	FromRevision int64
	ToRevision   int64
	Reason       string
	AtMs         int64
}

// Config controls every threshold/policy of the engine.
type Config struct {
	// Custom measures.
	MeasureMinDurMs float64
	MeasureMaxDurMs float64
	// Long tasks.
	LongTaskThresholdMs float64
	BlockingThresholdMs float64
	// Frames.
	FrameBudgetMs       float64
	DroppedFpsThreshold float64
	JankFpsThreshold    float64
	JankRunLength       int
	StallGapMs          int64
	// Windowing.
	TumbleMs int64 // 0 disables tumbling sub-windows
	// Clock.
	MaxClockJumpMs int64
	// Sampling.
	SampleRate float64
	SampleSeed uint64
	// Storage.
	CheckpointAfter int
}

// DefaultConfig returns sane production defaults:
//
//	MeasureMinDurMs        0     (accept any positive duration)
//	MeasureMaxDurMs        600000 (cap measures at 10 minutes)
//	LongTaskThresholdMs    50
//	BlockingThresholdMs    50
//	FrameBudgetMs          16.67 (one 60Hz frame)
//	DroppedFpsThreshold    50
//	JankFpsThreshold       30
//	JankRunLength          3
//	StallGapMs             1000
//	TumbleMs               0    (segment windows only)
//	MaxClockJumpMs         3600000 (1h; larger moves are flagged)
//	SampleRate             1
//	CheckpointAfter        100
func DefaultConfig() Config {
	return Config{
		MeasureMinDurMs:     0,
		MeasureMaxDurMs:     600000,
		LongTaskThresholdMs: 50,
		BlockingThresholdMs: 50,
		FrameBudgetMs:       1000.0 / 60.0,
		DroppedFpsThreshold: 50,
		JankFpsThreshold:    30,
		JankRunLength:       3,
		StallGapMs:          1000,
		TumbleMs:            0,
		MaxClockJumpMs:      3600 * 1000,
		SampleRate:          1,
		SampleSeed:          0,
		CheckpointAfter:     100,
	}
}

var _ = time.Now
