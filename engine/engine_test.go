package engine

import (
	"encoding/json"
	"math/rand"
	"os"
	"testing"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

func testCfg(dir string) Config {
	return Config{Dir: dir, WindowMs: 1000, GraceMs: 500, SampleSeed: 42, SampleRate: 1, ClockJumpThresholdMs: 300}
}

// mkBatch 构造批次：发送与接收时刻都取最大条目时间（客户端零时钟偏移）。
func mkBatch(id, user string, entries ...model.Entry) model.Batch {
	maxT := int64(0)
	for _, e := range entries {
		if e.ClientTime > maxT {
			maxT = e.ClientTime
		}
	}
	return model.Batch{BatchID: id, UserID: user, Entries: entries, ClientSentAt: maxT, ServerReceivedAt: maxT}
}

// pushBatch 构造空批次，仅用于推进水位线（不影响时钟偏移估计）。
func pushBatch(id, user string, recv int64) model.Batch {
	return model.Batch{BatchID: id, UserID: user, ServerReceivedAt: recv}
}

func mark(id, name string, t int64) model.Entry {
	return model.Entry{ID: id, Kind: model.KindMark, Name: name, ClientTime: t, PageID: "p1", NavID: "n1"}
}

func frame(id string, t int64, dur float64) model.Entry {
	return model.Entry{ID: id, Kind: model.KindFrame, ClientTime: t, Duration: dur, PageID: "p1", NavID: "n1"}
}

func windowsJSON(t *testing.T, ws []WindowResult) string {
	t.Helper()
	data, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// 场景：乱序 + 重复 + 迟到 与 按时间顺序一次性归集 结果一致。
func TestOutOfOrderDuplicateLateEqualsOneShot(t *testing.T) {
	// 基准：全部条目按时间顺序一个批次送达。
	entries := []model.Entry{
		mark("m1", "load:start", 100),
		frame("f1", 110, 16.7),
		frame("f2", 130, 16.7),
		mark("m2", "load:end", 600),
		{ID: "ms1", Kind: model.KindMeasure, Name: "load", ClientTime: 700, PageID: "p1", NavID: "n1"},
		frame("f3", 1200, 200), // 卡顿帧，窗口 1
	}
	oneShot := NewInMemory(t)
	if _, err := oneShot.Ingest(mkBatch("b-all", "u", entries...)); err != nil {
		t.Fatal(err)
	}
	if _, err := oneShot.Ingest(pushBatch("b-push", "u", 10000)); err != nil {
		t.Fatal(err)
	}
	want := windowsJSON(t, normalizeRevisions(oneShot.Windows()))

	// 乱序：批次内条目乱序；重复：同一批次重发、换批次 ID 重发；
	// 迟到：窗口 0 发布后才送达的条目。
	shuffled := append([]model.Entry(nil), entries...)
	rand.New(rand.NewSource(1)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	eng := NewInMemory(t)
	if _, err := eng.Ingest(mkBatch("b1", "u", shuffled[:4]...)); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(mkBatch("b1", "u", shuffled[:4]...)); err != nil { // 同批次 ID 重发
		t.Fatal(err)
	}
	if _, err := eng.Ingest(mkBatch("b1-dup", "u", shuffled[:4]...)); err != nil { // 换批次 ID 重发
		t.Fatal(err)
	}
	if _, err := eng.Ingest(mkBatch("b2", "u", frame("f3", 1200, 200))); err != nil {
		t.Fatal(err)
	}
	// 推进水位线使窗口 0 发布（此时 load:end 与 measure 尚未到达）
	if _, err := eng.Ingest(pushBatch("b2-push", "u", 3000)); err != nil {
		t.Fatal(err)
	}
	if len(eng.Windows()) == 0 {
		t.Fatal("window 0 should be published")
	}
	// 迟到条目：load:end 与 measure 在窗口 0 发布后到达
	revs, err := eng.Ingest(mkBatch("b3", "u", mark("m2", "load:end", 600),
		model.Entry{ID: "ms1", Kind: model.KindMeasure, Name: "load", ClientTime: 700, PageID: "p1", NavID: "n1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) == 0 {
		t.Fatal("late data must trigger window recomputation")
	}
	if revs[0].Reason != "late_data" {
		t.Fatalf("unexpected revision reason: %v", revs[0].Reason)
	}
	// 再推进水位线发布窗口 1
	if _, err := eng.Ingest(pushBatch("b4", "u", 10000)); err != nil {
		t.Fatal(err)
	}
	// 指标内容必须与一次性归集逐项相等（revision 号是合理的追溯差异）。
	got := windowsJSON(t, normalizeRevisions(eng.Windows()))
	if got != want {
		t.Fatalf("out-of-order/duplicate/late result diverges from one-shot:\nwant %s\ngot  %s", want, got)
	}
	// 窗口 0 被整体重算并留下可追溯记录
	found := false
	for _, r := range eng.Revisions() {
		if r.WindowStart == 0 && r.Reason == "late_data" && r.Revision == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("late-data recomputation must be traceable, revisions: %v", eng.Revisions())
	}
}

// normalizeRevisions 将窗口修订号归零，便于与一次性归集结果比较指标内容。
func normalizeRevisions(ws []WindowResult) []WindowResult {
	out := make([]WindowResult, len(ws))
	for i, w := range ws {
		w.Revision = 0
		out[i] = w
	}
	return out
}

func NewInMemory(t *testing.T) *Engine {
	t.Helper()
	eng, err := New(testCfg(""))
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// 增量状态必须与全量重算逐项相等。
func TestIncrementalEqualsFullRecompute(t *testing.T) {
	eng := NewInMemory(t)
	batches := []model.Batch{
		mkBatch("a", "u", mark("s", "m:start", 100), frame("f1", 150, 16.7)),
		mkBatch("b", "u", mark("e", "m:end", 400), frame("f2", 1300, 90)),
		mkBatch("c", "u", model.Entry{ID: "mm", Kind: model.KindMeasure, Name: "m", ClientTime: 450, PageID: "p1", NavID: "n1"}),
		pushBatch("d", "u", 5000),
	}
	for _, b := range batches {
		if _, err := eng.Ingest(b); err != nil {
			t.Fatal(err)
		}
	}
	inc := eng.Windows()
	full, err := eng.RecomputeAll()
	if err != nil {
		t.Fatal(err)
	}
	if windowsJSON(t, inc) != windowsJSON(t, full) {
		t.Fatalf("incremental != full recompute:\ninc  %s\nfull %s", windowsJSON(t, inc), windowsJSON(t, full))
	}
}

// 客户端时钟偏移与回拨：观测时间轴不得倒流。
func TestClockSkewAndBackwards(t *testing.T) {
	eng := NewInMemory(t)
	// 客户端时钟比服务端慢 500ms
	b1 := model.Batch{BatchID: "c1", UserID: "u", ClientSentAt: 1000, ServerReceivedAt: 1500,
		Entries: []model.Entry{frame("f1", 100, 0), frame("f2", 900, 0)}}
	if _, err := eng.Ingest(b1); err != nil {
		t.Fatal(err)
	}
	// 客户端时钟回拨：条目客户端时间小于之前，但服务端时间轴不得倒流
	b2 := model.Batch{BatchID: "c2", UserID: "u", ClientSentAt: 500, ServerReceivedAt: 1600,
		Entries: []model.Entry{frame("f3", 200, 0), frame("f4", 950, 0)}}
	if _, err := eng.Ingest(b2); err != nil {
		t.Fatal(err)
	}
	ts := eng.ServerTimes("u")
	for i := 1; i < len(ts); i++ {
		if ts[i] < ts[i-1] {
			t.Fatalf("server timeline must not go backwards: %v", ts)
		}
	}
	// 偏移 500：f1(100)->600, f2(900)->1400；回拨条目被钳制到 >= 1400
	if ts[0] != 600 {
		t.Fatalf("offset not applied, first server time = %d", ts[0])
	}
}

// 导航切分：应用内导航与挂起恢复落入不同观测单元。
func TestNavigationSplitInWindows(t *testing.T) {
	eng := NewInMemory(t)
	entries := []model.Entry{
		{ID: "nav1", Kind: model.KindLifecycle, Name: model.LcNavigate, ClientTime: 100, PageID: "p1", NavID: "n1"},
		frame("f1", 200, 16.7),
		{ID: "nav2", Kind: model.KindLifecycle, Name: model.LcNavigate, ClientTime: 300, PageID: "p1", NavID: "n2"},
		frame("f2", 400, 16.7),
		{ID: "sus", Kind: model.KindLifecycle, Name: model.LcSuspend, ClientTime: 500, PageID: "p1", NavID: "n2"},
		{ID: "res", Kind: model.KindLifecycle, Name: model.LcResume, ClientTime: 600, PageID: "p1", NavID: "n2"},
		frame("f3", 700, 16.7),
	}
	if _, err := eng.Ingest(mkBatch("n1", "u", entries...)); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(pushBatch("n2", "u", 5000)); err != nil {
		t.Fatal(err)
	}
	ws := eng.Windows()
	if len(ws) != 1 {
		t.Fatalf("want 1 window, got %d", len(ws))
	}
	if len(ws[0].Units) != 3 {
		keys := []string{}
		for k := range ws[0].Units {
			keys = append(keys, k)
		}
		t.Fatalf("want 3 observation units (n1, n2-seg0, n2-seg1), got %v", keys)
	}
}

// 重启恢复：从已提交位置继续，已吸收批次不被重复计入。
func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	b1 := mkBatch("r1", "u", mark("s", "m:start", 100), mark("e", "m:end", 400),
		model.Entry{ID: "mm", Kind: model.KindMeasure, Name: "m", ClientTime: 450, PageID: "p1", NavID: "n1"})
	if _, err := eng.Ingest(b1); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(mkBatch("r2", "u", frame("f1", 1200, 50))); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(pushBatch("r2p", "u", 5000)); err != nil {
		t.Fatal(err)
	}
	before := windowsJSON(t, eng.Windows())
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启
	eng2, err := New(testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := windowsJSON(t, eng2.Windows()); got != before {
		t.Fatalf("recovered state diverges:\nbefore %s\nafter  %s", before, got)
	}
	// 重发已吸收批次：不得重复计入
	if _, err := eng2.Ingest(b1); err != nil {
		t.Fatal(err)
	}
	if got := windowsJSON(t, eng2.Windows()); got != before {
		t.Fatal("re-delivered committed batch must not be counted twice")
	}
	// 重启后继续吸收新批次
	if _, err := eng2.Ingest(mkBatch("r3", "u", frame("f2", 1300, 16.7))); err != nil {
		t.Fatal(err)
	}
	if _, err := eng2.Ingest(pushBatch("r3p", "u", 9000)); err != nil {
		t.Fatal(err)
	}
	inc := eng2.Windows()
	full, err := eng2.RecomputeAll()
	if err != nil {
		t.Fatal(err)
	}
	if windowsJSON(t, inc) != windowsJSON(t, full) {
		t.Fatal("post-recovery incremental != full recompute")
	}
	if err := eng2.Close(); err != nil {
		t.Fatal(err)
	}
}

// 采样确定性：相同输入与种子，两次运行结果一致。
func TestSamplingDeterministicAcrossRuns(t *testing.T) {
	run := func() string {
		cfg := testCfg("")
		cfg.SampleRate = 0.5
		cfg.SampleSeed = 99
		eng, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var entries []model.Entry
		for i := 0; i < 100; i++ {
			entries = append(entries, frame("f"+string(rune(i)), int64(100+i*10), 16.7))
		}
		if _, err := eng.Ingest(mkBatch("s1", "u", entries...)); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Ingest(pushBatch("s2", "u", 9000)); err != nil {
			t.Fatal(err)
		}
		return windowsJSON(t, eng.Windows())
	}
	if run() != run() {
		t.Fatal("same seed and input must give identical results")
	}
}

// 崩溃恢复：WAL 完整但检查点丢失时，能从 WAL 重建已提交状态。
func TestCrashRecoveryWithoutCheckpoint(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(mkBatch("k1", "u", mark("s", "m:start", 100), mark("e", "m:end", 400),
		model.Entry{ID: "mm", Kind: model.KindMeasure, Name: "m", ClientTime: 450, PageID: "p1", NavID: "n1"})); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ingest(pushBatch("k2", "u", 5000)); err != nil {
		t.Fatal(err)
	}
	before := windowsJSON(t, normalizeRevisions(eng.Windows()))
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃后检查点丢失，仅 WAL 幸存
	if err := os.Remove(dir + "/checkpoint.json"); err != nil {
		t.Fatal(err)
	}
	eng2, err := New(testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	got := windowsJSON(t, normalizeRevisions(eng2.Windows()))
	if got != before {
		t.Fatalf("crash recovery must rebuild committed state from WAL:\nwant %s\ngot  %s", before, got)
	}
	// 已吸收批次仍不得重复计入
	if _, err := eng2.Ingest(mkBatch("k1", "u", mark("s", "m:start", 100), mark("e", "m:end", 400),
		model.Entry{ID: "mm", Kind: model.KindMeasure, Name: "m", ClientTime: 450, PageID: "p1", NavID: "n1"})); err != nil {
		t.Fatal(err)
	}
	if windowsJSON(t, normalizeRevisions(eng2.Windows())) != before {
		t.Fatal("committed batch re-delivery after crash must not double count")
	}
	if err := eng2.Close(); err != nil {
		t.Fatal(err)
	}
}
