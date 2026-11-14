# Champion 策略说明与配置指南

## 综合质量模式：client/server 链路

`strategy.settings.qualityMode` 控制本地 healthcheck 与客户端 QUIC 统计的综合选路。默认 `off`；本文后续第 1–15 节描述保留的旧模式。新模式的实现入口为 `app/router/strategy_champion_quality.go`，数据来源为 `app/observatory/quality.go` 和 `transport/internet/splithttp/quality.go`。

| 模式 | 实际选择 | 质量采集 |
| --- | --- | --- |
| `off`（默认） | 旧 Champion | 冷启动不启用采样 |
| `shadow` | 旧 Champion；另行记录新规则的建议 | 启用 |
| `select` | 新的综合质量规则 | 启用 |

模式名忽略首尾空格及大小写，未知值报配置错误。没有 observatory 时保留轮询语义。采集一经启用就随该 observatory 实例存活；热重载切回 `off` 会停止新规则参与选路，采样器在实例关闭时退出。

可直接参考 [champion-quality.json](examples/champion-quality.json)，将其中的 observatory、balancer 和路由规则合入现有客户端配置。该文件是配置片段，线路与入站继续由既有配置定义：例如现有 XHTTP + H3 出站为 `proxy-hk`、`proxy-sg`、`proxy-jp`，两个 selector 的 `proxy-` 前缀会匹配它们。

`probeURL` 应替换为你已有的、由 server 本地直接返回 2xx（如 204）的 healthcheck，并保留其 server 路由。将 `preferredTag` 改为实际偏好出站。示例 `network: "tcp,udp"` 是兜底规则，应放在现有直连、拦截等更具体的规则之后；也可将其条件改为仅匹配需要自动选路的入站或业务。

想先观察建议，可设为 `shadow`，并使用 `info` 日志级别；需要查看保留现任的原因时使用 `debug`。既有 `fallbackTag` 和人工 override 仍按 Balancer 的原规则处理。`preferredMaxDelayGap` 只影响旧模式；新模式使用下面固定的接近条件。

### 通过 `/debug/vars` 查看 Champion

启用了 metrics API 后，现有命令可以分别查看 observatory 原始状态和 Champion 状态：

```bash
curl -s http://127.0.0.1:11/debug/vars | jq '.observatory'
curl -s http://127.0.0.1:11/debug/vars | jq '.champion'
curl -s http://127.0.0.1:11/debug/vars | jq '.champion.out_us'
```

`.champion` 是按 balancer tag 索引的只读快照。`.observatory` 仍是旧的 healthcheck 状态；`.champion` 额外包含：

- `current`：实际接收新流的当前擂主；`quality_current`：综合质量模型记录的擂主。`shadow` 模式下两者可能不同。
- `preferred`、`mode`、`state`、`reason`：首选线路、模式、当前可靠性状态和最近一次决策理由。
- `challenger`、`challenge_kind`、`wins`、`required_wins`、`challenger_wins`、`challenger_required_wins`：正在挑战的线路、普通晋级或 preferred 接近回切、同类挑战的总票数和门槛、当前挑战者自己的票数和门槛（总门槛的一半，向上取整）。
- `last_evaluation_at`、`last_switch_at`、`challenge_started_at`、`cooldown_remaining_ms`：评估、切换、计票开始时间和剩余冷却；`last_switch_at` 只在擂主、代次或验证状态变化时更新，冷启动等待首选首个结果期间重复选择同一未验证线路时不刷新它，也不重置冷却；`snapshot_epoch/version` 是已用于决策的版本，`quality_snapshot_epoch/version` 是当前底层质量快照版本。
- `current_evidence_at`、`candidate_evidence_at`、`runtime_evidence_at`：本擂主任期内最近一次探测票所用的现任探测样本时间、当前挑战者最近一次探测票所用的证据时间、最近一次运行时票所用的现任运行时证据时间。下一张探测票需要比前两个时间更新的观测，下一张运行时票需要比第三个时间更新的观测。现任的两个时间在重新累计时保留、换擂主时清空；挑战者的时间随重新累计清空。
- `candidates[]`：每个候选的 `state`、`known`、`reason`、`reliability_rank`、`score_ms`、`recent_score_ms`、`probe_ms`、`probe_updated`、`transport_rtt_ms`、`transport_rtt_updated`、`loss_sent`、`loss_estimated`、`loss_updated`、`loss_corrected` 和证据时间。

没有有效样本的耗时字段为 `null`，不会用 0ms 或默认 500ms 冒充测量。`loss_sent` 和 `loss_estimated` 是客户端发送端判失视角，不能解读为精确双向丢包率。`current` 是当前实际选路，`quality_current` 是模型内部状态；排查 `shadow` 时优先比较这两个字段。`off` 模式下候选的 `score_ms` 是第 5 节的综合分数，与切换日志的 `*_score_ms` 同口径；observatory 的原始 `Delay` 在 `legacy_delay_ms`，存活状态在 `alive`。

### 数据口径

- standard 与 burst 均独立记录原始 healthcheck 结果，**新质量记录以请求无错误且 HTTP 2xx 为成功**，包括 server 的 204。非 2xx 是健康检查失败，不称为丢包。旧 observatory/overlay 的成功契约保留，旧 access log 的 `delay` 也保留原含义。
- 首版采集 XHTTP + H3 实际物理 QUIC 连接的握手耗时、平滑 RTT、波动、累计发送与判失变化、异常关闭。握手完成信号才算建连成功；复用子流不会重新计一次握手或复制包数。
- 普通目标网站的首响应、下载耗时及 server→目标的错误不作为新模型输入。上传/下载分离的 H3 连接分别采样，归入其 outbound；链式代理统计只归属实际传输 hop。
- keepalive 保活及已有 healthcheck 按时间运行，不分配业务试用配额，也不新增 server 协议。

### 排名与切换

先排除 `unavailable` / `recovering`，再比较可靠性等级，同级比较分数。所有候选都可挑战当前擂主。无有效 healthcheck 的指标为 `unknown`，不填入 500ms、1ms 或零丢包。冷启动可临时选择未验证线路；首选已在观测范围内、未确认故障，但还没有任何探测结果时，冷启动先按未验证（`quality_unverified`）使用首选，等首选的第一个结果（成功或失败）再做首次选择，避免先完成探测的其他候选抢先成为擂主；故障切换和代次变化不等待。现任只是缺少新观测时保留选择并暂停晋级；全不可用时返回空 tag 交给 fallback。

```text
P = healthcheck 均值 + healthPingJitterScale × 波动
D = 建连 / QUIC RTT 相对各自较早基线的劣化成本，取较大值
F = 探测、握手、异常关闭失败成本，取较大值
L = 客户端 QUIC 发送判失压力成本
S = P + D + max(F, L)
```

D 同时考虑均值与波动的增长。F/L 按有效样本数、时间覆盖与新鲜度降权；它们是选路成本，不是一次请求耗时。运行时窗口为 60 秒，每 5 秒采样；较早基线最多 10 分钟，排除最近 60 秒及有失败、loss 或修正的桶。healthcheck 保留最多 20 个样本，有效期按各出站实际探测节奏计算。

普通晋级要求可靠性持续更好，或同级下滚动分数与近期分数均改善至少 `max(20ms, 现任分数 × 15%)`。preferred 只有同级、新鲜且 `S_preferred - S_best <= max(10ms, S_best × 5%)` 才可获得偏好；不能挡住已经达到普通晋级门槛的其他候选。

普通晋级默认需要 4 票，且从首票起持续至少 20 秒；preferred 接近回切默认需要 6 票，持续至少 30 秒。性能切换间隔至少 30 秒。每个 5s 周期最多计一票，每票都要有与本次判断有关的新观测。一票只消耗它实际使用的观测：探测票消耗现任探测样本和挑战者证据，运行时票只消耗现任运行时证据，未用的观测留给后续同类票；现任的每个观测在整个擂主任期内最多支撑一票，切换类别变化、优势中断或评估中断后重新累计也不能再用。普通晋级有两种票型：

- 探测票（`probe`）：现任有新的探测样本，挑战者有新证据，并且用现任最新一次探测代替 P 后，挑战者仍领先晋级门槛（最新一次探测失败或挑战者可靠性等级更好也算领先）。探测间隔较长时窗口只有 2–3 个样本，单个慢样本会让 P 在整个探测有效期内偏高；最新样本恢复后就不再计票，所以单个慢样本最多计一票。探测票按探测节奏累计：探测间隔 29s 时普通晋级约需 1.5 分钟，10s 时约 30 秒。
- 运行时票（`runtime`）：现任有新的运行时证据（实际 RTT、握手、失败或 loss），并且双方运行时分量 `D + max(F, L)` 的差距本身（滚动分与近期分）达到晋级门槛，或挑战者可靠性等级更好。此时备用候选只需新鲜，票按 5s 周期累计，约 20 秒切换。现任实际 RTT 只略高于基线时，运行时证据与本次劣势无关，不计运行时票。

preferred 接近回切只计探测票，只要求现任有新的探测样本、首选有新证据，不比较最新样本。重复读取、无关线路更新、单次旧尖峰都不能补足票数；长于 10 秒没有评估会重新累计。同类晋级中挑战者轮换（例如两个相近的快节点交替领先）时保留总票数，但每一票仍按上面的规则要求新观测；每个挑战者单独记录已计票证据，轮换回来时要有自己的新证据才计探测票。晋级目标自身至少要拿到门槛一半的票（向上取整，普通晋级默认 2 票），只在最后一轮领先的候选不能兑现其他挑战者的票。切换类别变化时重新累计。探测稀疏时会更慢，不能保证固定 20–30 秒切换。

10 秒内两个独立、连续的同阶段物理连接故障，或两个不同周期的连续 healthcheck 失败，可确认不可用；明确故障在下一次新流选择时跳过性能冷却。握手失败、已建立连接故障和 healthcheck 分开累计，不能相加成“两次”。新握手成功只恢复握手阶段；本地 healthcheck 成功可验证已建立传输路径。恢复要求对应阶段 3 个不同有效周期成功，仅恢复比较资格。握手阶段失败后，现有 healthcheck 会临时绕过 mux/xmux，使用独立连接验证；完成后关闭该私有 H3 客户端。

### 丢包与日志

客户端读取的是**客户端发送端判失压力**，包含控制包、重发及 ACK 路径影响，不能称为精确下行或双向丢包率。有效性起点为至少 50 个发送包、3 个非空桶；500 包获得完整样本权重，仍按年龄衰减。迟到 ACK 导致计数回落、零分母或不合理增量时，该区间不生成普通比率，并降低窗口置信度。

初值为 `L = 500ms × 权重 × min(1, 判失压力 / 5%)`。5% 是归一化参考，不是判死线；持续 loss 可推动性能切换，单次尖峰恢复后不能继续累计持续劣化票。协议栈负责重发与拥塞控制；Champion 只改变后续新流的选择。

质量日志以 `champion quality` 开头，包含 `balancer`、`mode`、`reason`、旧/新分数、可用性状态、`old_last_failure`、可靠性等级、P/D/F/L 成本、`probe_ms`、`transport_rtt_ms`、`sender=client`、发送/判失计数、修正标记、更新时间、已用证据时间（`current_evidence_at`、`candidate_evidence_at`、`runtime_evidence_at`）、本次票型（`vote`：`none`、`probe`、`runtime` 或 `probe+runtime`）、总票数与门槛（`wins`/`required_wins`）、当前挑战者自身票数与门槛（`challenger_wins`/`challenger_required_wins`）以及冷却。缺失时延或分数打印 `unknown`；旧擂主不在候选集时（例如首次选择），旧擂主字段打印 `none`。时间字段使用不含空格的 RFC3339 毫秒格式，缺失时打印 `none`；`evidence_at` 是选中线路的综合证据时间，与状态接口候选的 `evidence_at` 同源，不是本票所用的观测；三个已用证据时间与状态接口同名字段同源，是本次决策后的值，晋级日志中记录的是被替换现任最后一票所用的观测。`old_last_failure` 是旧擂主最近一次探测失败原因，带引号，线路恢复后不清空，不等于本次决策的 `reason`。日志级别：稳态与观察为 Debug；shadow 中的建议变化为 Info；select 中替换已有擂主为 Warning，首次选择为 Info。切换理由包括 `quality_failover`、`quality_promoted`、`preferred_near`；`shadow` 中的新 tag 是建议结果。评分不依赖日志级别，正常稳态不会逐请求重复打印质量决定。

`qualityMode` 为 `shadow` 或 `select`，但 observatory 不提供质量数据时，启动时记录一次 Warning（`champion quality mode unavailable`，`effective_mode=off`），并按 `off` 运行。

这些阈值是工程初值。自动测试覆盖确定性状态回放与真实本机 H3 往返，尚未进行广域网丢包注入、双端 loss 回传或生产校准。详细验证结果记录在 `tasks/todo.md`。

## 旧模式参考（qualityMode=off）

本文档基于当前仓库实现整理，适用于需要查询以下问题的场景：

- Champion 是什么，和 `roundrobin`、`leastping` 的行为差异是什么。
- Champion 依赖哪些观测数据。
- Champion 在链路抖动、真实业务失败、恢复、全死场景下的行为是什么。
- `strategy.settings` 里的五个参数该怎么写，怎么调。

## 1. Champion 是什么

Champion 是一个“保住现任擂主，再谨慎切换”的 balancer strategy。

当前实现入口在：

- `app/router/config.go::(*BalancingRule).Build`
- `app/router/strategy_champion.go::(*ChampionStrategy).PickOutbound`

它的核心目标有三条：

1. 在多条可用线路之间保持稳定，减少来回切换。
2. 在 challenger 明显更优时完成晋级。
3. 在线路恢复后，让 preferred 节点按更保守的条件回切。

当前 Champion 独立实现于 `app/router/strategy_champion.go`，没有复用 `roundrobin` 的内部逻辑。相关语义见：

- `app/router/strategy_champion.go::(*ChampionStrategy).PickOutbound`
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallbackLocked`

## 2. Champion 依赖什么输入

Champion 依赖 observatory 输出的 `ObservationResult.Status`。

读取路径在：

- `app/router/strategy_champion.go::(*ChampionStrategy).loadObservation`
- `app/router/strategy_champion_support.go::newChampionObservation`

每个 candidate tag 最终会被整理成 `OutboundStatus`，Champion 主要消费以下字段：

- `Alive`
- `Delay`
- `LastTryTime`
- `LastSeenTime`
- `HealthPing`

其中：

- standard observatory 的状态写入路径在 `app/observatory/observer.go::(*Observer).updateStatusForResult`
- burst observatory 的状态写入路径在 `app/observatory/burst/burstobserver.go::(*Observer).createBaseResultLocked`

如果启用了 burst observatory，Champion 会优先使用 `HealthPing.Average / Deviation / Fail` 计算综合分数。逻辑在：

- `app/router/strategy_champion_support.go::(*championObservation).delayScore`
- `app/router/strategy_champion_support.go::championHealthPingScore`

## 3. Champion 的核心行为

### 3.1 无 observatory 时

Champion 会退化为普通轮询。

代码路径：

- `app/router/strategy_champion.go::(*ChampionStrategy).PickOutbound`
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallbackLocked`

这时不会使用健康观测结果。

- 若 `strategy.settings.preferredTag` 命中候选集，且当前还没有既有 champion，或旧 champion 已不在当前候选集，则首轮 round-robin fallback 会先从它启动
- 后续请求再继续按候选数组顺序轮转

observatory 读取失败或返回不支持的结果时，同样退化为轮询。轮询每次请求都会换 tag，因此不逐次记录切换：进入回退或回退原因变化时记录一次 Warning（`champion fallback to round robin`），恢复使用观测结果时记录一次 Info（`champion fallback ended`）。字段见第 13 节。

### 3.2 有 observatory 时

Champion 每次选择时会先构造三个角色：

- `preferred`：若 `strategy.settings.preferredTag` 命中候选集则优先用该 tag；否则退回候选数组中的第一个 tag
- `anchor`：当前 champion 仍可用时，继续沿用当前 champion；当前 champion 不可用时退回 preferred
- `best`：当前所有 alive candidate 中综合分数最低的 tag

代码路径：

- `app/router/strategy_champion_support.go::(*championObservation).preferred`
- `app/router/strategy_champion_support.go::(*championObservation).anchor`
- `app/router/strategy_champion_support.go::(*championObservation).best`
- `app/router/strategy_champion.go::(*ChampionStrategy).decideLocked`

observatory 的读取、决策与提交在同一次加锁内完成：提交顺序与读取顺序一致，较早读到的旧观测不会在新观测之后提交，因此不会重复计票，也不会撤销刚完成的晋级。内置 observatory 的 `GetObservation` 只复制内存状态，不做网络 I/O。日志在解锁后输出。

### 3.3 全部 candidate 都死掉时

Champion 会返回空 tag，由 balancer 的 `fallbackTag` 接管。

代码路径：

- `app/router/strategy_champion.go::(*ChampionStrategy).decideLocked`
- `app/router/strategy_champion.go::(*ChampionStrategy).pickObserved`
- `app/router/balancing.go::(*Balancer).PickOutbound`

只要 `fallbackTag` 已配置，balancer 会回退到该 tag。

## 4. Champion 的切换规则

### 4.1 Challenger 晋级规则

无论当前 champion 是否为 `preferred`，其他 candidate 都需要满足下面两层条件才能晋级：

第一层：当前快照里 challenger 显著更优。

代码在 `app/router/strategy_champion_support.go::(*championObservation).challengerWins`，规则是：

- challenger 必须和当前 anchor 不是同一个 tag
- challenger 分数必须满足 `bestDelay < anchorDelay * 4 / 7`
- challenger 分数必须满足 `bestDelay < anchorDelay - 100ms`

第二层：这个“显著更优”要在多个不同观测上连续出现。

代码在：

- `app/router/strategy_champion.go::(*ChampionStrategy).challengeLocked`
- `app/router/strategy_champion_support.go::(*championDuel).observe`
- `app/router/strategy_champion_support.go::championDuelKey`

计票规则：

- “不同观测”按参与比较的分数判断：anchor 分数、challenger tag、challenger 分数任一变化才计一票。观测在策略锁内读取并按顺序提交（见 3.2），同一份 observatory 结果不会被高并发请求重复记分
- runtime feedback 只刷新 `LastTryTime` / `LastSeenTime`，不产生新测量，因此时间戳变化不计票
- 连续两轮测量的分数完全相同时只计一票，极少数情况下晋级会多等一轮
- challenger 可以轮换：只要每次观测都有 candidate 满足第一层条件就继续累计总票数；任一次观测不满足即清零
- 每一票同时记给本轮的晋级目标：通常是 best；当前 champion 不是 preferred，且 preferred 与 best 满足 4.2 的第一层条件时记给 preferred
- 总票数达到 `candidateObservationCount` 时，只有本轮目标可以晋级，并且它自己至少要有一半票（向上取整；默认 4 票中至少 2 票）。只在最后一轮领先的候选不能兑现其他候选攒下的票，只会继续观察
- 晋级或 anchor 变化后胜场清零

preferred 作为本轮目标晋级时，日志为 `reason=preferred_reclaimed`，避免先切到 best 再被 preferred 回切。preferred 只在最后一轮接近 best 时，不会借普通挑战的票回切。

### 4.2 Preferred 回切规则

当前 champion 已经是非首选节点时，preferred 要满足下面两层条件才能回切：

第一层：preferred 不能“明显更差”；如果 preferred 本身更快，则不会被 `preferredMaxDelayGap` 阻止。

代码在 `app/router/strategy_champion_support.go::(*championObservation).preferredCanReclaim`，规则是：

- preferred 必须存活
- preferred 与当前 champion 不能是同一个 tag
- 当 `preferredDelay > anchorDelay` 时，要求 `preferredDelay - anchorDelay < preferredMaxDelayGap`
- 当 `preferredDelay <= anchorDelay` 时，不受 `preferredMaxDelayGap` 限制
- `anchorDelay > preferredDelay * 4 / 7`

第二层：这个“足够接近且更优”要连续出现在多个不同观测上，计票规则同 4.1。

代码在：

- `app/router/strategy_champion.go::(*ChampionStrategy).challengeLocked`
- `app/router/strategy_champion_support.go::(*championDuel).observe`

回切计数与 4.1 的普通挑战计数相互独立，同一次观测可以同时为两者计票；先达到门槛的规则生效。

## 5. Champion 的分数怎么算

当前 Champion 的分数来自 `app/router/strategy_champion_support.go::championHealthPingScore`。

### 5.1 没有 HealthPing 时

使用 `OutboundStatus.Delay`。

代码路径：

- `app/router/strategy_champion_support.go::(*championObservation).delayScore`
- `app/router/strategy_champion_support.go::championOutboundScore`：选路、切换日志和 `/debug/vars` 的 `score_ms` 共用

### 5.2 有 HealthPing 时

使用下面这套综合分数：

```text
score = average + deviation * scale + fail penalty
```

具体逻辑：

1. 基础值优先取 `HealthPing.Average`
2. 抖动惩罚取 `HealthPing.Deviation * healthPingJitterScale`
3. 失败惩罚按 `Fail / All` 比例叠加，最小值为 `50ms`

代码路径：

- `app/router/strategy_champion_support.go::championHealthPingScore`

这意味着：

- 平均 RTT 更低的线路有优势
- 抖动更大的线路会被额外扣分
- 失败率更高的线路也会被额外扣分

## 6. 真实业务成功与失败怎么影响 Champion

Champion 本身只读 observatory 结果，业务成功与失败通过 runtime feedback overlay 回写到 observatory。

主链如下：

- `common/session/outbound_signal.go` 产生 signal
- `app/dispatcher/outbound_feedback.go::SubmitOutboundSignal` 回写 observatory
- `app/observatory/runtime_feedback.go` 维护 overlay
- `app/observatory/observer.go::(*Observer).snapshotObservationStatusLocked` 合成 standard observatory 结果
- `app/observatory/burst/burstobserver.go::(*Observer).createResultLocked` 合成 burst observatory 结果

### 6.1 会写入 overlay 的业务信号

当前信号种类包括：

- `dial_failure`
- `mux_failure`
- `pre_relay_proxy_failure`
- `dial_success`
- `relay_success`

链路代码在：

- `common/session/outbound_signal.go`
- `app/dispatcher/outbound_feedback.go`

### 6.2 当前恢复与失败门槛

当前门槛定义在 `app/observatory/runtime_feedback.go`：

- `runtimeFeedbackDownThreshold = 2`
- `runtimeFeedbackRecoverThreshold = 7`

当前语义是：

- 冷启动时首个成功即可把 synthetic 状态拉成 alive
- 稳定期 2 次失败判 down
- down 之后 7 次成功恢复 alive

### 6.3 较新的 probe 或 health ping 如何影响 overlay

较新的 base probe / health ping 会重置 overlay 内存状态，并清空旧 streak。代码在：

- `app/observatory/runtime_feedback.go::(*runtimeFeedbackState).refreshFromBase`
- `app/observatory/runtime_feedback.go::(*runtimeFeedbackState).resetFromBase`
- `app/observatory/runtime_feedback.go::(*runtimeFeedbackOverlay).applyToStatus`

burst observatory 会把 `LastUpdateUnixNano` 一起传给 overlay，代码在：

- `app/observatory/burst/burstobserver.go::(*Observer).ReportOutboundSignal`
- `app/observatory/burst/burstobserver.go::(*Observer).baseStatusSnapshotForTagLocked`

这条语义的效果是：

- 旧失败不会跨越新的探测周期继续累计
- 新探测之后的单次失败，会从新的 streak 周期重新开始计数

## 7. 配置入口

Champion 的配置入口在 `routing.balancers[].strategy`。

JSON 解析入口在：

- `infra/conf/router.go::(*BalancingRule).Build`
- `infra/conf/router_strategy.go::(*strategyChampionConfig).Build`

运行时装配在：

- `app/router/config.go::(*BalancingRule).Build`

Proto 定义在：

- `app/router/config.proto::StrategyChampionConfig`

### 7.1 最小写法

```json
{
  "routing": {
    "balancers": [
      {
        "tag": "auto_proxy",
        "selector": ["proxy-"],
        "strategy": {
          "type": "champion"
        },
        "fallbackTag": "direct"
      }
    ]
  }
}
```

### 7.2 完整写法

```json
{
  "burstObservatory": {
    "subjectSelector": ["proxy-"],
    "pingConfig": {
      "destination": "https://www.google.com/generate_204",
      "connectivity": "https://www.google.com/generate_204",
      "interval": "30s",
      "sampling": 5,
      "timeout": "5s",
      "httpMethod": "HEAD"
    }
  },
  "routing": {
    "balancers": [
      {
        "tag": "auto_proxy",
        "selector": ["proxy-"],
        "strategy": {
          "type": "champion",
          "settings": {
            "preferredTag": "proxy-hk",
            "candidateObservationCount": 6,
            "preferredObservationCount": 8,
            "healthPingJitterScale": 1.5,
            "preferredMaxDelayGap": "100ms"
          }
        },
        "fallbackTag": "direct"
      }
    ]
  }
}
```

## 8. 五个参数怎么用

### 8.1 `candidateObservationCount`

含义：challenger 要连续赢多少个不同观测，才能晋级为 champion。期间 challenger 可以轮换，但晋级目标自己至少要赢其中一半（向上取整），见 4.1。

配置字段：

```json
"candidateObservationCount": 4
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion.go::(*ChampionStrategy).challengeLocked`
- `app/router/strategy_champion_support.go::(*championDuel).observe`

建议：

- `4`：默认值，稳态优先
- `5~6`：更保守，适合高抖动线路
- `2~3`：更积极，适合切换成本低的场景

### 8.2 `preferredObservationCount`

含义：preferred 接近回切（4.2）要连续赢多少个不同观测，才能从当前非 preferred champion 手里回切。普通挑战达到门槛、且本轮 preferred 接近 best 时，也可以经 4.1 回到 preferred；那条路径按 `candidateObservationCount` 计票，并要求 preferred 自己至少有一半票。

配置字段：

```json
"preferredObservationCount": 6
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion.go::(*ChampionStrategy).challengeLocked`
- `app/router/strategy_champion_support.go::(*championDuel).observe`

建议：

- `6`：默认值
- `7~10`：回切更保守
- `4~5`：回切更积极

### 8.3 `healthPingJitterScale`

含义：抖动惩罚倍数。只有使用 burst observatory 时，这个参数的价值最明显。

配置字段：

```json
"healthPingJitterScale": 1.0
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion_support.go::championHealthPingScore`

建议：

- `1.0`：默认值
- `1.2 ~ 1.8`：抖动线路较多时更稳
- `0.5 ~ 0.8`：更看重平均延迟

当前实现把 `<= 0` 统一收口为默认值，逻辑在：

- `app/router/strategy_champion_support.go::ChampionSettings.normalized`

### 8.4 `preferredMaxDelayGap`

含义：preferred 回切时，只在 preferred 比当前 champion 更慢的场景下，限制允许“向上选择”的最大分差；如果 preferred 本身更快，则不会被这个字段拦住。

配置字段：

```json
"preferredMaxDelayGap": "80ms"
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion_support.go::(*championObservation).preferredCanReclaim`

这个字段通过 `duration` 解析，常见写法：

- `"60ms"`
- `"80ms"`
- `"120ms"`
- `"1s"`

建议：

- `80ms`：默认值
- `60ms`：preferred 更慢时更谨慎
- `100~150ms`：允许 preferred 在略慢时也更容易回切

### 8.5 `preferredTag`

含义：显式指定默认首选擂主。

配置字段：

```json
"preferredTag": "proxy-hk"
```

默认值来源：

- 空字符串；代码在 `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion_support.go::(*championObservation).preferred`
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallbackLocked`

规则：

- 命中候选集时，作为 preferred 使用
- 未命中候选集时，退回候选数组首项
- 无 observatory 且当前没有既有 champion，或旧 champion 已不在当前候选集时，round-robin fallback 也会先从它启动

## 9. Partial settings 的行为

你可以只写一部分字段，其他字段会自动走默认值。

构建链在：

- `infra/conf/router_strategy.go::(*strategyChampionConfig).Build`
- `app/router/config.go::(*BalancingRule).Build`
- `app/router/strategy_champion_support.go::ChampionSettings.normalized`

示例：

```json
{
  "routing": {
    "balancers": [
      {
        "tag": "auto_proxy",
        "selector": ["proxy-"],
        "strategy": {
          "type": "champion",
          "settings": {
            "candidateObservationCount": 5
          }
        }
      }
    ]
  }
}
```

上面这段配置的实际效果是：

- `candidateObservationCount = 5`
- `preferredObservationCount = 6`
- `healthPingJitterScale = 1`
- `preferredMaxDelayGap = 80ms`
- `preferredTag = ""`

## 10. 推荐配置模板

### 10.1 高抖动线路，优先稳态

```json
"strategy": {
  "type": "champion",
  "settings": {
    "preferredTag": "proxy-hk",
    "candidateObservationCount": 6,
    "preferredObservationCount": 8,
    "healthPingJitterScale": 1.5,
    "preferredMaxDelayGap": "100ms"
  }
}
```

适用特点：

- 节点偶发低 RTT 很诱人
- 真实体验更在意稳定
- 切换代价较高

### 10.2 中等抖动，稳态和灵活性平衡

```json
"strategy": {
  "type": "champion",
  "settings": {
    "candidateObservationCount": 5,
    "preferredObservationCount": 6,
    "healthPingJitterScale": 1.2,
    "preferredMaxDelayGap": "80ms"
  }
}
```

### 10.3 线路整体稳定，偏向更快切换

```json
"strategy": {
  "type": "champion",
  "settings": {
    "candidateObservationCount": 3,
    "preferredObservationCount": 4,
    "healthPingJitterScale": 0.8,
    "preferredMaxDelayGap": "60ms"
  }
}
```

## 11. 什么时候建议配 burstObservatory

如果你希望 `healthPingJitterScale` 真正体现价值，建议配 `burstObservatory`。

原因在于：

- `burstObservatory` 会提供 `HealthPing.Average / Deviation / Fail`，写入路径在 `app/observatory/burst/burstobserver.go::(*Observer).createBaseResultLocked`
- Champion 的抖动惩罚逻辑直接依赖这几个字段，代码在 `app/router/strategy_champion_support.go::championHealthPingScore`

只用 standard observatory 时，Champion 主要消费的是 `Delay`。

## 12. 当前未开放到配置层的门槛

当前 observatory runtime feedback 的迟滞门槛还是代码常量，还没有配置入口。

定义位置在：

- `app/observatory/runtime_feedback.go`

当前固定值：

- `runtimeFeedbackDownThreshold = 2`
- `runtimeFeedbackRecoverThreshold = 7`

这部分会直接影响 Champion 看到的 alive / dead 状态。

## 13. 常见日志原因字段

Champion 切换时会打日志，代码在：

- `app/router/strategy_champion.go::(*ChampionStrategy).logChampionSwitch`

格式：

```text
champion switched balancer=<tag> reason=<reason> old_tag=<tag|none> new_tag=<tag|none> old_score_ms=<分数> new_score_ms=<分数>
```

- `*_score_ms` 是第 5 节的综合分数，不是实测延迟；`dead` 表示 observatory 判定不可用，`unknown` 表示没有观测（算法按 500ms 参与比较）或旧擂主已不在候选集，`none` 表示没有对应擂主
- 旧擂主为空（启动、规则重载、全部失效后恢复）时用 Info，其余切换用 Warning
- tag 字段（`balancer`、`old_tag`、`new_tag`、`champion`、`challenger`）在没有对应 tag 时输出 `none`；tag 本身是 `none`，或含空白、`=`、引号、不可打印字符时加引号输出，例如 `new_tag="out us"`

当前 `reason` 包括：

- `no_previous_champion`：没有旧擂主，选择 preferred
- `champion_removed`：旧擂主不在当前候选集，回到 preferred
- `champion_unavailable`：旧擂主失效，回到 preferred
- `challenger_promoted`
- `preferred_reclaimed`
- `best_selected_from_non_anchor`：旧擂主和 preferred 都不可用，选择 best
- `all_candidates_dead`

轮询回退只在进入和退出时记录：

```text
champion fallback to round robin balancer=<tag> cause=<cause> quality_mode=<mode> error=<"错误"|none>
champion fallback ended balancer=<tag> cause=<cause> champion=<tag|none>
```

`cause` 取值为 `observatory_unavailable`、`observation_error`、`observation_unsupported`。回退期间擂主记录随轮询变化；`champion` 是回退结束后选出的擂主，它与最后一次轮询结果相同时不会再有 `champion switched` 日志。

这些日志适合用来排查：

- 当前为什么切换了
- 当前为什么没有切换
- 当前是不是已经退回 fallbackTag

## 14. 配置示例：结合 routing rule 使用

```json
{
  "burstObservatory": {
    "subjectSelector": ["proxy-"],
    "pingConfig": {
      "destination": "https://www.google.com/generate_204",
      "connectivity": "https://www.google.com/generate_204",
      "interval": "30s",
      "sampling": 5,
      "timeout": "5s",
      "httpMethod": "HEAD"
    }
  },
  "routing": {
    "domainStrategy": "AsIs",
    "balancers": [
      {
        "tag": "auto_proxy",
        "selector": ["proxy-"],
        "strategy": {
          "type": "champion",
          "settings": {
            "candidateObservationCount": 6,
            "preferredObservationCount": 8,
            "healthPingJitterScale": 1.5,
            "preferredMaxDelayGap": "100ms"
          }
        },
        "fallbackTag": "direct"
      }
    ],
    "rules": [
      {
        "type": "field",
        "domain": ["geosite:openai"],
        "balancerTag": "auto_proxy"
      }
    ]
  }
}
```

## 15. 查询建议

后续如果要继续查询某个 Champion 行为，建议按下面这条源码主链去看：

1. `app/router/strategy_champion.go`
2. `app/router/strategy_champion_support.go`
3. `app/observatory/runtime_feedback.go`
4. `app/observatory/observer.go`
5. `app/observatory/burst/burstobserver.go`
6. `app/dispatcher/outbound_feedback.go`

这条链能覆盖：

- 配置如何进入 Champion
- Champion 如何计算分数
- observatory 如何合成 alive / dead
- 真实业务成功与失败如何影响选路
