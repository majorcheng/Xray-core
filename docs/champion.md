# Champion 策略说明与配置指南

本文档基于当前仓库实现整理，适用于需要查询以下问题的场景：

- Champion 是什么，和 `roundrobin`、`leastping` 的行为差异是什么。
- Champion 依赖哪些观测数据。
- Champion 在链路抖动、真实业务失败、恢复、全死场景下的行为是什么。
- `strategy.settings` 里的四个参数该怎么写，怎么调。

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

这时不会使用健康观测结果，只按顺序在候选 tag 里轮转。

### 3.2 有 observatory 时

Champion 每次选择时会先构造三个角色：

- `preferred`：候选数组中的第一个 tag
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

第一层：preferred 足够接近当前 champion。

代码在 `app/router/strategy_champion_support.go::(*championObservation).preferredCanReclaim`，规则是：

- preferred 必须存活
- preferred 与当前 champion 不能是同一个 tag
- `abs(anchorDelay - preferredDelay) < preferredMaxDelayGap`
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

## 8. 四个参数怎么用

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

含义：preferred 回切时，允许与当前 champion 之间存在的最大分差。

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
- `60ms`：回切更谨慎
- `100~150ms`：回切更容易触发

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

## 10. 推荐配置模板

### 10.1 高抖动线路，优先稳态

```json
"strategy": {
  "type": "champion",
  "settings": {
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
