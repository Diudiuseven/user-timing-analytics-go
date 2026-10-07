// Package engine 提供归集管线：乱序/重复/迟到吸收、窗口发布与重算、
// 增量与全量一致、检查点恢复。
//
// 工作方式：
//   - 批次到达后先做幂等去重与确定性采样，再做客户端时钟校正；
//   - 被吸收的条目按 (服务端时间, 稳定键) 有序进入每用户日志，并追加到 WAL；
//   - 窗口在水位线越过 窗口结束+宽限期 后发布；迟到数据落入已发布窗口时，
//     该窗口被整体重算替换（不出现部分字段更新的混合状态），并记录 Revision；
//   - 检查点持久化水位线、已发布窗口与重算记录；重启后重放 WAL 恢复日志，
//     已吸收批次按稳定键与批次 ID 去重，不会被重复计入。
package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Diudiuseven/user-timing-analytics-go/clock"
	"github.com/Diudiuseven/user-timing-analytics-go/dedup"
	"github.com/Diudiuseven/user-timing-analytics-go/metrics"
	"github.com/Diudiuseven/user-timing-analytics-go/model"
	"github.com/Diudiuseven/user-timing-analytics-go/session"
)

// Config 引擎配置。
type Config struct {
	Dir        string  // WAL 与检查点目录；为空则纯内存
	WindowMs   int64   // 窗口大小（服务端时间）
	GraceMs    int64   // 宽限期：窗口结束后等待迟到数据的时间
	SampleSeed uint64  // 采样种子
	SampleRate float64 // 采样率 (0,1]
	// ClockJumpThresholdMs 是可容忍的最大传输延迟：批次表观时钟偏移
	// 向上跳变超过该阈值时判定为客户端时钟回拨并采用新偏移。
	ClockJumpThresholdMs int64
	Metrics              metrics.Config // 指标计算配置
}

func (c *Config) withDefaults() {
	if c.WindowMs <= 0 {
		c.WindowMs = 60000
	}
	if c.SampleRate <= 0 {
		c.SampleRate = 1
	}
	if c.ClockJumpThresholdMs <= 0 {
		c.ClockJumpThresholdMs = 30000
	}
	if c.Metrics == (metrics.Config{}) {
		c.Metrics = metrics.DefaultConfig()
	}
}

// WindowResult 一个窗口的发布结果。
type WindowResult struct {
	WindowStart int64                         `json:"window_start"`
	Revision    int                           `json:"revision"` // 重算次数，0 为首次发布
	Units       map[string]*model.UnitMetrics `json:"units"`
}

// Revision 记录一次窗口重算（可追溯）。
type Revision struct {
	WindowStart int64  `json:"window_start"`
	Revision    int    `json:"revision"`
	Reason      string `json:"reason"`
}

// storedEntry 是已吸收（去重、采样、时钟校正后）的条目。
type storedEntry struct {
	Entry      model.Entry `json:"entry"`
	ServerTime int64       `json:"server_time"`
}

// walRecord 是追加到 WAL 的批次记录。
type walRecord struct {
	BatchID   string        `json:"batch_id"`
	UserID    string        `json:"user_id"`
	Entries   []storedEntry `json:"entries"`
	Watermark int64         `json:"watermark"` // 本批次处理后的水位线
}

// checkpoint 是持久化的已提交状态。
type checkpoint struct {
	Watermark int64                   `json:"watermark"`
	Published map[int64]*WindowResult `json:"published"`
	Revisions []Revision              `json:"revisions"`
}

// Engine 归集引擎。
type Engine struct {
	cfg        Config
	filter     *dedup.Filter
	correctors map[string]*clock.Corrector // 每用户时钟校正器
	log        map[string][]storedEntry    // 每用户有序条目日志
	committed  map[string]struct{}         // 已吸收批次 ID
	watermark  int64
	published  map[int64]*WindowResult
	revisions  []Revision
	wal        *os.File
}

// New 创建（或从 Dir 恢复）引擎。
func New(cfg Config) (*Engine, error) {
	cfg.withDefaults()
	e := &Engine{
		cfg:        cfg,
		filter:     dedup.NewFilter(cfg.SampleSeed, cfg.SampleRate),
		correctors: make(map[string]*clock.Corrector),
		log:        make(map[string][]storedEntry),
		committed:  make(map[string]struct{}),
		published:  make(map[int64]*WindowResult),
	}
	if cfg.Dir == "" {
		return e, nil
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := e.recover(); err != nil {
		return nil, err
	}
	w, err := os.OpenFile(filepath.Join(cfg.Dir, "wal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	e.wal = w
	return e, nil
}

// recover 重放 WAL 并加载检查点，然后对齐二者状态。
func (e *Engine) recover() error {
	walPath := filepath.Join(e.cfg.Dir, "wal.jsonl")
	var maxServer int64
	hasWal := false
	if f, err := os.Open(walPath); err == nil {
		hasWal = true
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			var rec walRecord
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				return fmt.Errorf("wal replay: %w", err)
			}
			e.committed[rec.BatchID] = struct{}{}
			for _, se := range rec.Entries {
				e.filter.Accept(se.Entry) // 重建已见键
				e.insert(rec.UserID, se)
				if se.ServerTime > maxServer {
					maxServer = se.ServerTime
				}
			}
			if rec.Watermark > maxServer {
				maxServer = rec.Watermark
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return err
		}
	}
	cpPath := filepath.Join(e.cfg.Dir, "checkpoint.json")
	cpLoaded := false
	if data, err := os.ReadFile(cpPath); err == nil {
		var cp checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			return fmt.Errorf("checkpoint load: %w", err)
		}
		cpLoaded = true
		e.watermark = cp.Watermark
		e.published = cp.Published
		e.revisions = cp.Revisions
		if e.published == nil {
			e.published = make(map[int64]*WindowResult)
		}
	}
	if !cpLoaded && hasWal {
		// 检查点缺失（如崩溃后丢失）：以 WAL 记录的水位线恢复，
		// 保证已吸收数据对应的窗口能够重新发布。
		e.watermark = maxServer
	}
	// 对齐：崩溃可能发生在 WAL 落盘与检查点落盘之间，
	// 对所有含条目且已终态的窗口重算，使发布状态与日志一致。
	for _, ws := range e.finalWindowsWithEntries() {
		e.recomputeWindow(ws, "recovery")
	}
	return nil
}

// Ingest 吸收一个批次，返回因迟到数据而触发的窗口重算记录。
func (e *Engine) Ingest(b model.Batch) ([]Revision, error) {
	if _, dup := e.committed[b.BatchID]; dup {
		return nil, nil // 已吸收批次：幂等忽略
	}
	if b.ServerReceivedAt == 0 {
		b.ServerReceivedAt = time.Now().UnixMilli()
	}
	corr := e.correctors[b.UserID]
	if corr == nil {
		corr = clock.NewCorrector(e.cfg.ClockJumpThresholdMs)
		e.correctors[b.UserID] = corr
	}
	corr.ObserveBatch(b.ClientSentAt, b.ServerReceivedAt)

	rec := walRecord{BatchID: b.BatchID, UserID: b.UserID}
	dirty := make(map[int64]struct{})
	maxT := b.ServerReceivedAt
	// 批次内条目顺序任意：先按 (客户端时间, 稳定键) 排序再校正，
	// 保证处理结果与批次内顺序无关。
	accepted := make([]model.Entry, 0, len(b.Entries))
	for _, ent := range b.Entries {
		if e.filter.Accept(ent) {
			accepted = append(accepted, ent)
		}
	}
	sort.Slice(accepted, func(i, j int) bool {
		if accepted[i].ClientTime != accepted[j].ClientTime {
			return accepted[i].ClientTime < accepted[j].ClientTime
		}
		return dedup.StableKey(accepted[i]) < dedup.StableKey(accepted[j])
	})
	for _, ent := range accepted {
		se := storedEntry{Entry: ent, ServerTime: corr.ToServer(ent.ClientTime)}
		rec.Entries = append(rec.Entries, se)
		e.insert(b.UserID, se)
		dirty[se.ServerTime/e.cfg.WindowMs] = struct{}{}
		if se.ServerTime > maxT {
			maxT = se.ServerTime
		}
	}
	if maxT > e.watermark {
		e.watermark = maxT
	}
	rec.Watermark = e.watermark
	if err := e.appendWAL(rec); err != nil {
		return nil, err
	}
	e.committed[b.BatchID] = struct{}{}

	// 迟到数据落入已发布窗口：整体重算。
	var revs []Revision
	before := len(e.revisions)
	for ws := range dirty {
		if _, ok := e.published[ws]; ok {
			e.recomputeWindow(ws, "late_data")
		}
	}
	// 水位线推进后，发布新终态窗口。
	for _, ws := range e.finalWindowsWithEntries() {
		if _, ok := e.published[ws]; !ok {
			e.publishWindow(ws)
		}
	}
	revs = append(revs, e.revisions[before:]...)
	if err := e.writeCheckpoint(); err != nil {
		return nil, err
	}
	return revs, nil
}

// insert 把条目按 (ServerTime, 稳定键) 有序插入用户日志。
func (e *Engine) insert(userID string, se storedEntry) {
	lst := e.log[userID]
	key := dedup.StableKey(se.Entry)
	i := sort.Search(len(lst), func(i int) bool {
		if lst[i].ServerTime != se.ServerTime {
			return lst[i].ServerTime > se.ServerTime
		}
		return dedup.StableKey(lst[i].Entry) >= key
	})
	lst = append(lst, storedEntry{})
	copy(lst[i+1:], lst[i:])
	lst[i] = se
	e.log[userID] = lst
}

// finalWindowsWithEntries 返回含条目且已越过宽限期的窗口下标，有序。
func (e *Engine) finalWindowsWithEntries() []int64 {
	set := make(map[int64]struct{})
	for _, lst := range e.log {
		for _, se := range lst {
			ws := se.ServerTime / e.cfg.WindowMs
			if (ws+1)*e.cfg.WindowMs+e.cfg.GraceMs <= e.watermark {
				set[ws] = struct{}{}
			}
		}
	}
	out := make([]int64, 0, len(set))
	for ws := range set {
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// publishWindow 首次发布窗口结果。
func (e *Engine) publishWindow(ws int64) {
	res := e.computeWindow(ws)
	res.Revision = 0
	e.published[ws] = res
}

// recomputeWindow 整体重算窗口：结果变化时替换并记录 Revision。
func (e *Engine) recomputeWindow(ws int64, reason string) {
	res := e.computeWindow(ws)
	old, ok := e.published[ws]
	if !ok {
		res.Revision = 0
		e.published[ws] = res
		return
	}
	if windowEqual(old, res) {
		return // 结果确定不变：保持已发布状态
	}
	res.Revision = old.Revision + 1
	e.published[ws] = res
	e.revisions = append(e.revisions, Revision{WindowStart: ws, Revision: res.Revision, Reason: reason})
}

// computeWindow 确定性管线：对全量日志做会话切分，聚合落在窗口内的条目。
// widx 为窗口下标（窗口起点 = widx * WindowMs）。
// 同一函数服务于增量发布、迟到重算与全量重算，保证结果逐项一致。
func (e *Engine) computeWindow(widx int64) *WindowResult {
	splitter := session.NewSplitter()
	aggs := make(map[model.UnitKey]*metrics.Aggregator)
	users := make([]string, 0, len(e.log))
	for u := range e.log {
		users = append(users, u)
	}
	sort.Strings(users)
	start := widx * e.cfg.WindowMs
	end := start + e.cfg.WindowMs
	for _, u := range users {
		for _, se := range e.log[u] {
			unit := splitter.Assign(u, clock.CorrectedEntry{Entry: se.Entry, ServerTime: se.ServerTime})
			if se.ServerTime < start || se.ServerTime >= end {
				continue
			}
			agg := aggs[unit]
			if agg == nil {
				agg = metrics.NewAggregator(e.cfg.Metrics)
				aggs[unit] = agg
			}
			agg.Add(se.Entry, se.ServerTime)
		}
	}
	res := &WindowResult{WindowStart: start, Units: make(map[string]*model.UnitMetrics)}
	for unit, agg := range aggs {
		res.Units[unitString(unit)] = agg.Result(unit)
	}
	return res
}

// Windows 返回已发布窗口结果（按窗口起点有序）。
func (e *Engine) Windows() []WindowResult {
	out := make([]WindowResult, 0, len(e.published))
	for _, w := range e.published {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowStart < out[j].WindowStart })
	return out
}

// Revisions 返回全部重算记录。
func (e *Engine) Revisions() []Revision { return append([]Revision(nil), e.revisions...) }

// ServerTimes 返回某用户已吸收条目的服务端时间轴（应单调不减）。
func (e *Engine) ServerTimes(userID string) []int64 {
	lst := e.log[userID]
	out := make([]int64, len(lst))
	for i, se := range lst {
		out[i] = se.ServerTime
	}
	return out
}

// RecomputeAll 从日志全量重算所有已发布窗口，结果必须与增量状态逐项相等。
func (e *Engine) RecomputeAll() ([]WindowResult, error) {
	out := make([]WindowResult, 0, len(e.published))
	for _, ws := range e.sortedPublished() {
		res := e.computeWindow(ws)
		res.Revision = e.published[ws].Revision
		out = append(out, *res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowStart < out[j].WindowStart })
	return out, nil
}

func (e *Engine) sortedPublished() []int64 {
	out := make([]int64, 0, len(e.published))
	for ws := range e.published {
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Close 提交检查点并关闭 WAL。
func (e *Engine) Close() error {
	if err := e.writeCheckpoint(); err != nil {
		return err
	}
	if e.wal != nil {
		return e.wal.Close()
	}
	return nil
}

func (e *Engine) appendWAL(rec walRecord) error {
	if e.wal == nil {
		return nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := e.wal.Write(append(data, '\n')); err != nil {
		return err
	}
	return e.wal.Sync()
}

func (e *Engine) writeCheckpoint() error {
	if e.cfg.Dir == "" {
		return nil
	}
	cp := checkpoint{Watermark: e.watermark, Published: e.published, Revisions: e.revisions}
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	tmp := filepath.Join(e.cfg.Dir, "checkpoint.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(e.cfg.Dir, "checkpoint.json"))
}

func unitString(k model.UnitKey) string {
	return fmt.Sprintf("%s|%s|%s|%d", k.UserID, k.PageID, k.NavID, k.Segment)
}

func windowEqual(a, b *WindowResult) bool {
	if a.WindowStart != b.WindowStart || len(a.Units) != len(b.Units) {
		return false
	}
	// 序列化比较，避免逐字段反射。
	da, err1 := json.Marshal(a.Units)
	db, err2 := json.Marshal(b.Units)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(da) == string(db)
}
