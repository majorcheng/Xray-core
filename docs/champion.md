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

### 数据口径

- standard 与 burst 均独立记录原始 healthcheck 结果，**新质量记录以请求无错误且 HTTP 2xx 为成功**，包括 server 的 204。非 2xx 是健康检查失败，不称为丢包。旧 observatory/overlay 的成功契约保留，旧 access log 的 `delay` 也保留原含义。
- 首版采集 XHTTP + H3 实际物理 QUIC 连接的握手耗时、平滑 RTT、波动、累计发送与判失变化、异常关闭。握手完成信号才算建连成功；复用子流不会重新计一次握手或复制包数。
- 普通目标网站的首响应、下载耗时及 server→目标的错误不作为新模型输入。上传/下载分离的 H3 连接分别采样，归入其 outbound；链式代理统计只归属实际传输 hop。
- keepalive 保活及已有 healthcheck 按时间运行，不分配业务试用配额，也不新增 server 协议。

### 排名与切换

先排除 `unavailable` / `recovering`，再比较可靠性等级，同级比较分数。所有候选都可挑战当前擂主。无有效 healthcheck 的指标为 `unknown`，不填入 500ms、1ms 或零丢包。冷启动可临时选择未验证线路；现任只是缺少新观测时保留选择并暂停晋级；全不可用时返回空 tag 交给 fallback。

```text
P = healthcheck 均值 + healthPingJitterScale × 波动
D = 建连 / QUIC RTT 相对各自较早基线的劣化成本，取较大值
F = 探测、握手、异常关闭失败成本，取较大值
L = 客户端 QUIC 发送判失压力成本
S = P + D + max(F, L)
```

D 同时考虑均值与波动的增长。F/L 按有效样本数、时间覆盖与新鲜度降权；它们是选路成本，不是一次请求耗时。运行时窗口为 60 秒，每 5 秒采样；较早基线最多 10 分钟，排除最近 60 秒及有失败、loss 或修正的桶。healthcheck 保留最多 20 个样本，有效期按各出站实际探测节奏计算。

普通晋级要求可靠性持续更好，或同级下滚动分数与近期分数均改善至少 `max(20ms, 现任分数 × 15%)`。preferred 只有同级、新鲜且 `S_preferred - S_best <= max(10ms, S_best × 5%)` 才可获得偏好；不能挡住已经达到普通晋级门槛的其他候选。

普通晋级默认需要 4 个有新证据的周期，且从首票起持续至少 20 秒；preferred 接近回切默认需要 6 个周期，持续至少 30 秒。性能切换间隔至少 30 秒。重复读取、无关线路更新、单次旧尖峰都不能补足票数；长于 10 秒没有评估会重新累计。探测稀疏时会更慢，不能保证固定 20–30 秒切换。

10 秒内两个独立、连续的同阶段物理连接故障，或两个不同周期的连续 healthcheck 失败，可确认不可用；明确故障在下一次新流选择时跳过性能冷却。握手失败、已建立连接故障和 healthcheck 分开累计，不能相加成“两次”。新握手成功只恢复握手阶段；本地 healthcheck 成功可验证已建立传输路径。恢复要求对应阶段 3 个不同有效周期成功，仅恢复比较资格。握手阶段失败后，现有 healthcheck 会临时绕过 mux/xmux，使用独立连接验证；完成后关闭该私有 H3 客户端。

### 丢包与日志

客户端读取的是**客户端发送端判失压力**，包含控制包、重发及 ACK 路径影响，不能称为精确下行或双向丢包率。有效性起点为至少 50 个发送包、3 个非空桶；500 包获得完整样本权重，仍按年龄衰减。迟到 ACK 导致计数回落、零分母或不合理增量时，该区间不生成普通比率，并降低窗口置信度。

初值为 `L = 500ms × 权重 × min(1, 判失压力 / 5%)`。5% 是归一化参考，不是判死线；持续 loss 可推动性能切换，单次尖峰恢复后不能继续累计持续劣化票。协议栈负责重发与拥塞控制；Champion 只改变后续新流的选择。

质量日志以 `champion quality` 开头，包含 `mode`、`reason`、旧/新分数、P/D/F/L 成本、`probe_ms`、`transport_rtt_ms`、`sender=client`、发送/判失计数、修正标记、更新时间、胜场与冷却。缺失时延打印 `unknown`。切换理由包括 `quality_failover`、`quality_promoted`、`preferred_near`；`shadow` 中的新 tag 是建议结果。评分不依赖日志级别，正常稳态不会逐请求重复打印质量决定。

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
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallback`

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
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallback`

这时不会使用健康观测结果。

- 若 `strategy.settings.preferredTag` 命中候选集，且当前还没有既有 champion，或旧 champion 已不在当前候选集，则首轮 round-robin fallback 会先从它启动
- 后续请求再继续按候选数组顺序轮转

### 3.2 有 observatory 时

Champion 每次选择时会先构造三个角色：

- `preferred`：若 `strategy.settings.preferredTag` 命中候选集则优先用该 tag；否则退回候选数组中的第一个 tag
- `anchor`：当前 champion 仍可用时，继续沿用当前 champion；当前 champion 不可用时退回 preferred
- `best`：当前所有 alive candidate 中综合分数最低的 tag

代码路径：

- `app/router/strategy_champion_support.go::(*championObservation).preferred`
- `app/router/strategy_champion_support.go::(*championObservation).anchor`
- `app/router/strategy_champion_support.go::(*championObservation).best`
- `app/router/strategy_champion.go::(*ChampionStrategy).selectObserved`

### 3.3 全部 candidate 都死掉时

Champion 会返回空 tag，由 balancer 的 `fallbackTag` 接管。

代码路径：

- `app/router/strategy_champion.go::(*ChampionStrategy).selectObserved`
- `app/router/strategy_champion.go::(*ChampionStrategy).commitAllCandidatesDead`
- `app/router/balancing.go::(*Balancer).PickOutbound`

只要 `fallbackTag` 已配置，balancer 会回退到该 tag。

## 4. Champion 的切换规则

### 4.1 Challenger 晋级规则

当前 champion 是 `preferred` 时，其他 candidate 需要满足下面两层条件才能晋级：

第一层：当前快照里 challenger 显著更优。

代码在 `app/router/strategy_champion_support.go::(*championObservation).challengerWins`，规则是：

- challenger 必须和当前 anchor 不是同一个 tag
- challenger 分数必须满足 `bestDelay < anchorDelay * 4 / 7`
- challenger 分数必须满足 `bestDelay < anchorDelay - 100ms`

第二层：这个“显著更优”要在多个不同观测快照上连续出现。

代码在：

- `app/router/strategy_champion.go::(*ChampionStrategy).decideChallenge`
- `app/router/strategy_champion.go::(*ChampionStrategy).recordDuelWin`
- `app/router/strategy_champion_support.go::(*championObservation).duelKey`

当前实现按“不同观测快照”计数，避免同一份 observatory 结果被高并发请求重复记分。

### 4.2 Preferred 回切规则

当前 champion 已经是非首选节点时，preferred 要满足下面两层条件才能回切：

第一层：preferred 不能“明显更差”；如果 preferred 本身更快，则不会被 `preferredMaxDelayGap` 阻止。

代码在 `app/router/strategy_champion_support.go::(*championObservation).preferredCanReclaim`，规则是：

- preferred 必须存活
- preferred 与当前 champion 不能是同一个 tag
- 当 `preferredDelay > anchorDelay` 时，要求 `preferredDelay - anchorDelay < preferredMaxDelayGap`
- 当 `preferredDelay <= anchorDelay` 时，不受 `preferredMaxDelayGap` 限制
- `anchorDelay > preferredDelay * 4 / 7`

第二层：这个“足够接近且更优”要连续出现在多个不同观测快照上。

代码在：

- `app/router/strategy_champion.go::(*ChampionStrategy).decidePreferredReclaim`
- `app/router/strategy_champion.go::(*ChampionStrategy).recordDuelWin`

## 5. Champion 的分数怎么算

当前 Champion 的分数来自 `app/router/strategy_champion_support.go::championHealthPingScore`。

### 5.1 没有 HealthPing 时

使用 `OutboundStatus.Delay`。

代码路径：

- `app/router/strategy_champion_support.go::(*championObservation).delayScore`

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

含义：challenger 要连续赢多少个不同观测快照，才能晋级为 champion。

配置字段：

```json
"candidateObservationCount": 4
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion.go::(*ChampionStrategy).decideChallenge`
- `app/router/strategy_champion.go::(*ChampionStrategy).recordDuelWin`

建议：

- `4`：默认值，稳态优先
- `5~6`：更保守，适合高抖动线路
- `2~3`：更积极，适合切换成本低的场景

### 8.2 `preferredObservationCount`

含义：preferred 要连续赢多少个不同观测快照，才能从当前非 preferred champion 手里回切。

配置字段：

```json
"preferredObservationCount": 6
```

默认值来源：

- `app/router/strategy_champion_support.go::defaultChampionSettings`

实际作用位置：

- `app/router/strategy_champion.go::(*ChampionStrategy).decidePreferredReclaim`
- `app/router/strategy_champion.go::(*ChampionStrategy).recordDuelWin`

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
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallback`

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

当前常见 `reason` 包括：

- `round_robin_fallback`
- `challenger_promoted`
- `preferred_reclaimed`
- `best_selected_from_non_anchor`
- `all_candidates_dead`

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
