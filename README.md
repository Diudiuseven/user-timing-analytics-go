# user-timing-analytics-go

前端性能观测数据的归集与指标分析引擎。上报端把用户计时条目（mark/measure）、
长任务记录、帧率采样和页面生命周期事件成批发送到服务端，引擎把这些数据收敛成
**可复现、可增量、可恢复** 的窗口化指标结果。

## 架构

```
批次 ──▶ dedup(幂等去重+确定性采样) ──▶ clock(客户端时钟校正) ──▶ 每用户有序日志 + WAL
                                                                    │
              ┌─────────────────────────────────────────────────────┘
              ▼
       session(会话/导航切分) ──▶ metrics(度量/长任务/帧率) ──▶ 窗口发布 / 迟到重算
                                                                    │
                                              checkpoint(水位线+已发布窗口+重算记录)
```

| 包 | 职责 |
|---|---|
| `model` | 上报条目、批次、观测单元、指标结果等核心类型 |
| `dedup` | 稳定键去重与种子确定性采样 |
| `clock` | 客户端时钟偏移估计与回拨修正 |
| `session` | 会话与导航切分，把条目归属到观测单元 |
| `metrics` | 自定义度量、长任务归因、帧率刻画 |
| `engine` | 归集管线：乱序/重复/迟到吸收、窗口发布与重算、检查点恢复 |

## 数据模型

`model.Batch` 携带同一用户的一批 `model.Entry`：

| 字段 | 含义 |
|---|---|
| `Entry.ID` | 客户端生成的稳定唯一 ID，是去重键（缺失时退化为内容指纹） |
| `Entry.Kind` | `mark` / `measure` / `longtask` / `frame` / `lifecycle` |
| `Entry.ClientTime` | 事件发生时的客户端时钟（ms） |
| `Entry.Duration` | measure/longtask 时长（ms）；frame 为帧间隔（ms） |
| `Entry.PageID` / `Entry.NavID` | 页面标识 / 导航标识（应用内导航时 NavID 变化） |
| `Batch.ClientSentAt` / `ServerReceivedAt` | 客户端发送时刻 / 服务端接收时刻，用于时钟偏移估计 |

## 指标语义

### 观测单元（UnitKey）

`用户 | 页面 | 导航 | 生命周期段`。同一用户在不同页面、同一页面内的应用内导航
（NavID 变化）之间归属不同单元；`suspend`/`unload` 关闭单元，`resume`/
`bfcache_restore` 开启 **新段**（段号 +1），挂起前后的指标不会串在一起。
缺少 navigate 事件时按 PageID/NavID 变化隐式切分。

### 自定义度量（Measures）

- `measure` 条目 `Duration > 0` 时直接采用；否则按名称配对 `<name>:start` 与
  `<name>:end` 标记计算时长（起点取不晚于度量记录的最后一个 start，终点取起点
  之后的第一个 end）。
- 未完成度量 **不按零值参与统计**，按原因分别计数（`UnitMetrics.Incomplete`）：
  - `missing_start`：缺少起点标记；
  - `missing_end`：缺少终点标记；
  - `abnormal_duration`：时长 ≤ 0 或超过 `MaxMeasureDurationMs`。
- 完成的度量按名称聚合出 `count/sum/min/max`。

### 长任务（LongTasks）

- 重叠区间先合并再计时：`total_busy_ms` 是区间 **并集** 时长，重叠部分不重复计时；
  `task_count` 为原始条数，`longest_ms` 为最长单任务。
- 归因（按窗口重叠量切分，导航优先）：
  - `by_navigation`：落在导航窗口（单元导航起点后 `NavigationWindowMs`）内的忙碌时长；
  - `by_interaction`：落在交互窗口（`interaction:*` 标记后 `InteractionWindowMs`）内的忙碌时长；
  - `unattributed`：其余忙碌时长。

### 帧率（Frames）

- `avg_fps`：1000 / 平均帧间隔；
- `dropped_ratio`：帧间隔超过 1.5 × `FrameBudgetMs` 的占比（掉帧）；
- `jank_count`：帧间隔超过 `JankThresholdMs` 的卡顿帧数；
- `longest_stall_ms`：最长单帧停顿。

## 一致性保证

- **乱序**：批次内条目先按 (客户端时间, 稳定键) 排序再处理；每用户日志按
  (服务端时间, 稳定键) 有序，结果与到达顺序无关。
- **重复**：条目按稳定键去重；批次按 BatchID 幂等忽略；重试与重复上报不会重复计入。
- **迟到**：窗口在水位线越过 `窗口结束 + GraceMs` 后发布。迟到数据落入已发布窗口时，
  该窗口被 **整体重算替换**（不出现部分字段更新的混合状态），并追加
  `Revision{window, revision, reason}` 可追溯记录；重算后指标与按时间顺序
  一次性归集的结果逐项相等。
- **时钟偏移与回拨**：以批次级时间对估计偏移，取迄今最小表观偏移（传输延迟只会
  增大表观偏移）；表观偏移向上跳变超过 `ClockJumpThresholdMs` 判定为客户端时钟
  回拨，采用新偏移保证观测时间轴不倒流，修正次数记录在 `Corrector.Adjustments`。
  表观偏移一致的迟到批次不会被误判。
- **采样**：`SampleRate` + `SampleSeed` 经 FNV-1a + splitmix64 混合哈希决定保留与否，
  相同输入与相同种子下逐次结果一致。
- **增量 = 全量**：增量发布、迟到重算与 `RecomputeAll()` 全量重算使用同一个确定性
  管线函数，结果逐项相等（有测试保证）。
- **重启恢复**：吸收的批次追加到 `wal.jsonl`（含处理后水位线）；检查点
  `checkpoint.json` 持久化水位线、已发布窗口与重算记录。重启后重放 WAL 重建
  有序日志与去重集合，已吸收批次不会被重复计入；检查点丢失时以 WAL 中的水位线
  恢复并重新发布终态窗口。

## 配置项（engine.Config）

| 配置 | 默认 | 说明 |
|---|---|---|
| `Dir` | 空（纯内存） | WAL 与检查点目录 |
| `WindowMs` | 60000 | 窗口大小（服务端时间 ms） |
| `GraceMs` | 0 | 窗口结束后等待迟到数据的宽限期 |
| `SampleSeed` | 0 | 采样种子 |
| `SampleRate` | 1 | 采样率 (0,1] |
| `ClockJumpThresholdMs` | 30000 | 可容忍的最大传输延迟；表观偏移向上跳变超过该值判定为客户端时钟回拨 |
| `Metrics.MaxMeasureDurationMs` | 60000 | 度量时长上限，超过视为异常 |
| `Metrics.FrameBudgetMs` | 16.7 | 基准帧间隔 |
| `Metrics.JankThresholdMs` | 50 | 卡顿帧阈值 |
| `Metrics.InteractionWindowMs` | 200 | 交互归因窗口 |
| `Metrics.NavigationWindowMs` | 3000 | 导航归因窗口 |

## 使用示例

```go
eng, _ := engine.New(engine.Config{
    Dir: "/var/lib/uta", WindowMs: 60000, GraceMs: 30000,
    SampleSeed: 42, SampleRate: 0.1,
    Metrics: metrics.DefaultConfig(),
})
revs, _ := eng.Ingest(batch)   // revs：迟到数据触发的窗口重算记录
windows := eng.Windows()       // 已发布窗口结果
full, _ := eng.RecomputeAll()  // 全量重算，与 windows 逐项相等
eng.Close()                    // 提交检查点
```

## 测试

```sh
go test ./...
```

覆盖：乱序、重复（同批次重发/换批次 ID 重发）、迟到（窗口重算与追溯记录）、
客户端时钟偏移与回拨、导航切分（应用内导航/挂起恢复/bfcache）、
增量与全量一致、采样确定性、重启恢复与崩溃后检查点丢失恢复。
