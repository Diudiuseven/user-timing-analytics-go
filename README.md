# user-timing-analytics-go

前端性能观测数据的**归集与指标分析引擎**（Go）。上报端把用户计时条目
（User Timing marks/measures）、长任务（long tasks）、帧率采样与页面生命周期
事件成批上报，服务端将其收敛为**可复现、可增量、可恢复**的指标结果。

## 能力一览

| # | 需求 | 实现方式 |
|---|------|----------|
| 1 | 乱序 / 重复 / 迟到 | 归集前按稳定键去重；时间线以**纯函数**从事件集按时间重建，与到达顺序无关；迟到数据并入后重算受影响窗口 |
| 2 | 会话 / 导航切分 | 以 `(user, session, page)` 归组，按客户端 `navId` 与 SPA 导航标记切导航，按 `freeze/pagehide → resume/pageshow(persisted)/bfcache_restore` 切**段** |
| 3 | 自定义度量 | 起止标记配对；区分 `complete / missing_start / missing_end / bad_duration / orphan_record`；异常时长不按零值入统 |
| 4 | 长任务与帧率 | 长任务先做区间并集（重叠/重复不重复计时），再按交互/导航窗口精确分摊；帧率输出均值、最低、P5、掉帧数、卡顿次数、最长卡顿时长 |
| 5 | 时钟偏移 / 跳变 | 每会话用批次到达样本的**中位数**估偏移（抗延迟批次、顺序无关）；单调归一化钳制回拨；已发布窗口被迟到数据改变时整体重算并记 `Revision` |
| 6 | 幂等去重 / 采样 | 批次键 + 条目身份键 + 条目载荷键三级去重；采样为 `FNV-1a → splitmix64` 的确定性哈希，同速率同种子结果一致 |
| 7 | 增量 = 全量 / 重启恢复 | 增量摄取与全量重算走**同一条全量重建代码路径**；批次仅追加日志 + 原子状态快照，重启重放日志从已提交位置继续 |
| 8 | 自动化测试 / 文档 | 覆盖乱序、重复、迟到、时钟回拨、导航切分、修订、重启恢复、采样可复现；见 [docs/](docs/) |

## 包结构

```
model/      上报条目、归一化事件、配置等数据契约
timesync/   时钟偏移中位数估计 + 单调（回拨钳制）归一化
dedupe/     批次 / 条目身份键 / 条目载荷键
sampling/   确定性、可复现采样
session/    会话时间线重建：导航与挂起/恢复段切分
measure/    自定义度量解析与未完成原因分类
longtask/   长任务区间并集与交互/导航窗口归因
framerate/  掉帧、卡顿、最长卡顿等帧率刻画
aggregate/  可合并数值草图 + 窗口结果装配
store/      仅追加批次日志 + 原子状态快照（内存 / 文件后端）
engine/     总装：摄取管线、窗口版本与修订、全量对账、重启恢复
```

## 一致性模型（关键设计）

每次 `Ingest`：

1. 按**批次键**丢弃重复批次；按会话做确定性采样；
2. 按**条目身份键 + 载荷键**丢弃重复条目（重试、以及被重新打包进新批次的重复条目）；
3. 把接受的批次**先持久化**到仅追加日志（fsync）；
4. 运行一次**全量对账（reconcile）**：从日志中所有已提交批次重新做时钟归一化、
   时间线重建与指标聚合。

正因为「增量」与「全量」共用同一条无副作用的全量重建路径，二者结果**逐项相等**，
且对乱序、迟到天然免疫。

**已发布窗口的迟到更新**绝不做字段级局部 patch：引擎构建完整新结果，
与旧结果做确定性内容比较；若变化，则：

- 窗口修订号 `Revision` 单调 +1（持久化，重启不回退）；
- 追加一条 `Revision{Window, FromRevision, ToRevision, Reason, AtMs}` 审计记录，
  由 `IngestReport.Revisions` 返回。

窗口结果要么是旧的完整值，要么是新的完整值，不存在半更新混合态。

## 快速上手

```go
cfg := model.DefaultConfig()
// cfg.SampleRate = 0.2; cfg.SampleSeed = 42  // 可选：20% 会话采样
st, _ := store.OpenFile("./data")            // 或 store.NewMemory()
eng, _ := engine.New(cfg, st)

b := model.Batch{
    BatchID: "batch-001", UserID: "u1", SessionID: "s1", PageID: "p1",
    NavID: "nav-1", ServerTimeMs: time.Now().UnixMilli(),
    Entries: []model.Entry{
        {Kind: model.KindMark, Name: "api-start", StartMs: 1700000000000},
        {Kind: model.KindMark, Name: "api-end",   StartMs: 1700000000120},
        // Detail 编码 "起始标记,结束标记"
        {Kind: model.KindMeasure, Name: "api", StartMs: 1700000000001,
         Detail: "api-start,api-end"},
        {Kind: model.KindLongTask, StartMs: 1700000000200, DurMs: 120},
        {Kind: model.KindFrame, StartMs: 1700000000300, DurMs: 50, Fps: 20},
        {Kind: model.KindLifecycle, StartMs: 1700000000400, Life: model.LifeFreeze},
    },
}
rep, _ := eng.Ingest(b)
// rep.AcceptedBatch / rep.DuplicateBatch / rep.SampledOut / rep.Revisions

for _, w := range eng.Results() {
    // w.Key 唯一标识 (user,session,page,nav,segment)
    // w.Measures / w.LongTasks / w.Frames / w.Revision
}
```

重启时只需对同一目录再次 `engine.New(cfg, st)`：日志自动重放，已吸收批次不会重复计入。

## 测试

```bash
go test ./...                 # 全部单元 + 集成测试
go test -race ./...           # 并发竞态检测
go test ./... -coverprofile=cov.out && go tool cover -func=cov.out
```

覆盖场景：乱序收敛、批次/条目重复（含跨批次重打包）、迟到并入、缺失起止标记、
异常时长、长任务重叠合并与交互归因、掉帧/卡顿、客户端时钟回拨、导航与挂起/bfcache
切分、滚动窗口、已发布窗口的整体重算与修订审计、增量=全量、文件日志重启恢复、
采样可复现与并发摄取。

## 观测单元（窗口）

窗口由 `model.WindowKey{UserID, SessionID, PageID, NavID, Segment, TumbleIdx}`
唯一确定：

- **导航**：客户端 `navId` 变化，或出现名为 `app-navigation` 的 SPA 导航标记
  （`Detail` 可显式给出新导航 id）。
- **段**：同一导航内遇到挂起 / 恢复边界会切出新段。冻结 / 切后台前后的指标
  **绝不串接**；bfcache 恢复同样开新段。若挂起后未见显式 `resume` 却继续来了
  普通事件，第一条事件会触发**隐式恢复**开新段。

## 文档

- [docs/metrics.md](docs/metrics.md) —— 各指标语义与配置项说明
