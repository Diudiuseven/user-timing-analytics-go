# 指标语义与配置参考

本文件说明引擎产出的每个指标的含义、未完成 / 异常情况的区分方式，以及
`model.Config` 中每个配置项的作用与默认值。

所有时间戳均为**毫秒**。上报时使用客户端墙钟 unix epoch ms；服务端经时钟校正后
统一到服务端轴。

## 1. 自定义度量（User Timing measures）

一个 measure 记录通过 `Entry.Detail = "起始标记名,结束标记名"` 指定起止标记；
解析只在**同一个导航段内**进行，标记不会跨导航 / 跨段匹配。

结果分类（`model.SegStatus`）互斥且明确：

| 状态 | 含义 | 是否计入时长统计 |
|------|------|------------------|
| `complete` | 找到起止标记，且时长在 sane 边界内 | 是，进入数值草图 |
| `missing_start` | 段内不存在该起始标记 | 否，只按原因计数 |
| `missing_end` | 起始标记之后不存在同名结束标记 | 否，只按原因计数 |
| `bad_duration` | 标记都存在但结束早于起点（倒置），或时长 ≤ 0 / 越界 | 否，`DurMs` 置 0，绝不按零值参与时长统计 |
| `organ_record` | measure 记录没有给出任何标记名 | 否，只按原因计数 |

每个度量名产出一个 `aggregate.MeasureAgg`：

- `Complete`：所有完整样本的 `count/sum/min/max/mean/p50/p95/p99`（精确分位数，
  草图保留全量样本，天然可合并）；
- `Incomplete`：按上述原因分别计数；`IncompleteTotal` 为未完成总数。

> 选择最近的确定性起始标记（同名多个时按时序取最后，键序打破并列），结束标记取
> 起点之后第一个；并列完全由稳定键序决定，因此重放 / 乱序输入结果一致。

## 2. 长任务（long tasks）

原始长任务条目先做**区间并集**（`longtask.Merge`）：重叠、相邻或重复的条目合并
成不重叠区间，阻塞时间至多计一次——两条覆盖同一时段的长任务不会把阻塞时间翻倍。

随后对每个并集区间按归因窗口切割（`longtask.Attribute`）：

- **交互窗口**：`kind=interaction` 条目给出的 `[start, end]`（`source` 为交互来源
  id），窗口 id 形如 `ixn:<source>`；
- **导航窗口**：未被任何交互窗口覆盖的部分归入 `nav:<navId>`。

一个窗口的 `aggregate.LongTaskAgg`：

| 字段 | 语义 |
|------|------|
| `TaskCount` | 并集后的长任务区间数（非原始条目数） |
| `TotalBlockedMs` | 去重后的阻塞总时长 |
| `MaxBlockedMs` | 单个并集区间的最大时长 |
| `ByWindow` | 按交互 / 导航窗口分摊的阻塞时长；所有份额之和恰为去重总阻塞时长 |

## 3. 帧率（frames）

输入为逐帧采样（时间戳 + 瞬时 fps，可选帧时长）。输出 `model.FrameStat`：

| 字段 | 语义 |
|------|------|
| `SampleCount` | 参与统计的帧数 |
| `AvgFps` | 平均帧率（仅作参考，不能单独刻画卡顿） |
| `MinFps` | 最低帧率 |
| `P5Fps` | 帧率分布的 5 分位（低端尾部，反映最差体验） |
| `DroppedFpsThreshold` 相关 → `DroppedFrames` | 慢于 `DroppedFpsThreshold`（fps）或帧时长超过 `FrameBudgetMs` 的帧数 |
| `JankEvents` | **卡顿事件数**：连续至少 `JankRunLength` 帧慢于 `JankFpsThreshold` 构成一次卡顿；相邻卡顿帧间隔超过 `StallGapMs` 视为另一次独立卡顿 |
| `LongestStallMs` | 最长一次卡顿覆盖的墙钟时长（含一帧预算的收尾） |

分析是对按时间排序样本的确定性折叠，乱序样本会先排序，故逐次结果一致。

## 4. 窗口结果 `aggregate.WindowResult`

| 字段 | 语义 |
|------|------|
| `Key` | `(user, session, page, nav, segment, tumbleIdx)`；`TumbleIdx=-1` 表示按段窗口 |
| `WindowStart/End` | 该段内最早事件起点 / 最晚事件终点（服务端轴） |
| `Measures` | 每个自定义度量名的聚合 |
| `LongTasks` / `Frames` | 第 2、3 节指标 |
| `Closed` | 窗口是否已关闭（段已挂起 / 导航已切换） |
| `Revision` | 单调修订号，初版为 0；已发布窗口被迟到数据改变时 +1 |
| `ComputedAtMs` | 计算时刻（仅运维信息，不参与窗口内容相等比较） |

## 5. 时钟处理

- **偏移估计**：每个会话收集 `serverReceiveMs - clientSendMs` 样本
  （`clientSendMs` 取批次内最晚条目时间），取**中位数**。中位数对少数高延迟批次
  稳健，且是顺序无关的确定性统计量。
- **单调归一化**：把客户端墙钟加上偏移后，保证观测轴单调不减——任何回拨 / NTP
  负跳变都会被钳制到已观测最大时间；超过 `MaxClockJumpMs` 的跳变（正/负）会被
  标记。正跳变（可能是真实挂起）保留，负跳变钳制。
- 归一化始终在**按客户端时间排序**的流上进行，与批次到达顺序无关。

## 6. 去重与采样

三级稳定键（`dedupe`）：

1. 批次键 `userId|b|batchId`：重试批次精确吸收一次；
2. 条目身份键 `batchId|e|index|payload`：同批次重试中的逐条目幂等；
3. 条目载荷键 `kind|name|start|dur|life|persisted|fps|source`：内容完全相同、但被
   重新打包进新批次 id 的重复条目也会被折叠（批次内不同位置的同载荷仍视为不同
   条目，保留首次出现的确定顺序）。

采样（`sampling`）默认作用于**会话级**（同一会话的所有条目同生共死，保证会话
指标完整）：`mix64(FNV-1a(seed || key)) < rate * 2^64`。原始 FNV-1a 对短顺序键有
分布偏斜，故再过一次 splitmix64（Stafford mix13）双射雪崩。相同速率与种子下
逐次结果完全一致；更换种子即轮换入样集合。

## 7. 持久化与恢复

- 每个被接受的批次在被吸收前以一整行 JSON 追加进 `journal.jsonl` 并 fsync；
- 状态（修订号、已发布窗口、去重集合、时钟样本、提交计数）写入 `state.json`，
  采用「写临时文件 + rename」原子替换，杜绝半写入状态；
- 重启 / 首次打开都通过重放日志恢复：日志本身即已吸收批次的事实来源，因此重放
  幂等，已吸收批次不会被重复计入；`CheckpointAfter` 控制每隔多少个已提交批次做
  一次快照（仅加速重启，不影响正确性）。

## 8. 配置项参考（`model.Config`）

| 配置项 | 默认值 | 含义 |
|--------|--------|------|
| `MeasureMinDurMs` | `0` | 完整度量允许的最小时长（含）；0 表示不设下限 |
| `MeasureMaxDurMs` | `600000` | 完整度量允许的最大时长（10 分钟）；0 表示不设上限 |
| `LongTaskThresholdMs` | `50` | 长任务阈值，短于此值的区间不纳入；并集后再按阈值过滤 |
| `BlockingThresholdMs` | `50` | 阻塞判定阈值（保留供交互阻塞指标扩展） |
| `FrameBudgetMs` | `1000/60 ≈ 16.67` | 单帧预算；帧时长超预算即计为掉帧 |
| `DroppedFpsThreshold` | `50` | 低于该 fps 计为掉帧 |
| `JankFpsThreshold` | `30` | 低于该 fps 计为「慢帧」候选 |
| `JankRunLength` | `3` | 连续慢帧达到该数构成一次卡顿事件 |
| `StallGapMs` | `1000` | 慢帧间隔超过该值视为两次独立卡顿 |
| `TumbleMs` | `0` | 段内滚动子窗口大小（毫秒），按**服务端校正轴的 epoch 边界**对齐（桶 `[k·TumbleMs,(k+1)·TumbleMs)`，`TumbleIdx=k`）；0 表示仅按导航段开窗。迟到数据总是落入其校正时间对应的固定桶，因此桶号稳定、不随到达顺序变化 |
| `MaxClockJumpMs` | `3600000` | 超过该幅度的时钟跳变被标记；回拨一律钳制 |
| `SampleRate` | `1` | 采样率 `[0,1]`；1 全保留、0 全丢弃 |
| `SampleSeed` | `0` | 采样种子，决定入样集合；同种子逐次一致 |
| `CheckpointAfter` | `100` | 每吸收多少批次做一次状态快照 |

## 9. 修订与可追溯性

对已通过 `Engine.Publish(key)` 发布的窗口，迟到数据导致内容变化时：

- 本次 `Ingest` 的返回值 `IngestReport.Revisions` 含一条修订记录；
- 记录含窗口键、`FromRevision/ToRevision`、原因（如 `ingest:<batchId>`）与时间戳；
- 修订号持久化，重启重放后不会回退；窗口始终是「旧完整值」或「新完整值」。
