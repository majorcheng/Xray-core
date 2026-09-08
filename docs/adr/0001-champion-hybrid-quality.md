# ADR 0001: Champion 综合链路质量选路

- 状态：Accepted；客户端首版已实现，阈值仍为待生产校准的工程初值。最终验证记录见 `tasks/todo.md`。
- 日期：2026-09-06；更新：2026-09-07。
- 源码基线：`1e8e0429cfc943f481349930c5776f0005fb00b7`。
- 目标：复用 server 本地 healthcheck，结合正常流量连接的传输统计，选择可靠且网络质量稳定的 client/server 链路。
- 相关资料：[当前 Champion](../champion.md)、[领域术语](../../CONTEXT.md)、[指标口径](../glossary.md)。

## 1. 部署前提与实现前基线

用户明确：当前 observatory 固定 URL 是由自己的 server 本地直接返回的 healthcheck，不访问外部目标站点。设计按此部署前提处理；没有读取线上 URL、server 路由配置或实际响应，不能把仓库默认 URL 当作用户配置。

探测覆盖的路径为：

```text
client observatory → 候选代理链路 / XHTTP + H3 → server 本地 healthcheck → 返回 client
```

因此现有探测可以成为 client/server 的主动质量基线。其耗时仍包含必要建链、传输、代理处理和 server 调度，不是纯网络 RTT；QUIC 原生 RTT 用来补充传输层视角。

| 实现前事实（off 模式保留） | 基线源码位置 |
| --- | --- |
| blackhole 的 `response.type = "health"` 在本地生成 HTTP 204；不拨号外部目标 | `proxy/blackhole/blackhole.go:56` |
| blackhole handler 写回后延迟关闭；204 无 body，不应拿连接寿命代替响应耗时 | `proxy/blackhole/blackhole.go:40` |
| standard observatory 经指定 outbound 发 GET，计时到响应头返回并关闭 Body | `app/observatory/observer.go:189,229` |
| burst 使用配置方法，HEAD 测到响应头，GET 还读取 Body | `app/observatory/burst/ping.go:55` |
| 两种 observatory 当前都不检查 HTTP 状态码；HTTP 请求无错误即记成功 | `app/observatory/observer.go:236`、`app/observatory/burst/ping.go:68` |
| access log 的 `delay` 是分派时读取的 observatory 快照 | `app/dispatcher/default.go:151,589` |
| runtime feedback 已有成功/失败事件，`DelayMs` 尚无实际计时的生产写入点 | `common/session/outbound_signal.go:106` |
| relay 上行读取与下行写入共用成功标记；不代表远端已经回包 | `app/proxyman/outbound/relay_state.go:19,40` |
| runtime overlay 的失败/恢复阈值按事件累计，固定为 2/7 | `app/observatory/runtime_feedback.go:12,103` |
| 非 preferred 擂主下主要检查 preferred 回切，第三条最佳线路缺少正常挑战路径 | `app/router/strategy_champion.go:118` |
| burst 评分优先使用 HealthPing，不能仅靠回填 `DelayMs` 接通完整评分 | `app/router/strategy_champion_support.go:169` |
| 缺少某 tag 的观测时，旧策略使用默认 500ms；新模式需明确 unknown | `app/router/strategy_champion_support.go:169` |
| 旧策略已按双方观测时间和分数去重相同 duel；新模型需进一步限定有效时间桶及原子提交 | `app/router/strategy_champion.go:262`、`app/router/strategy_champion_test.go:338` |

HTTP 403 等响应仍可让旧 observatory 记成功。新增的独立质量记录要求请求无错误且返回 2xx，覆盖当前 server 的 204 healthcheck；非 2xx 记为健康检查失败，不称为网络丢包。旧消费者和 access log 的语义保留。

## 2. 设计决策

1. 每个既有 balancer 一个擂主；所有候选均可参与质量比较，切换只影响新流。
2. 复用当前 server 本地 healthcheck，作为所有候选都可获得的主动延迟、波动、失败和恢复证据。
3. 从正常流量使用的物理 QUIC 连接读取 RTT、loss 和活动量，补充实际传输条件下的质量；可归因的建链/传输失败独立记录。
4. 只评价 client/server 路径。目标网站首响应、server→目标连接和完整下载时间不进入评分；首版也不为这些数据新增采集器或目标对照组。
5. 保留现有 keepalive 作为物理连接保活，复用 observatory 的探测调度；首版不需要独立 OPTIONS 心跳、echo 协议或约 1% 用户业务试用。
6. 可靠性和综合质量优先；仅在可靠性同级、证据新鲜且质量接近时偏好 preferred。恢复可用只恢复竞争资格，不直接触发回切。
7. 统计按固定时间桶发布快照，重复读取同一证据不能增加挑战胜场。入口为 `strategy.settings.qualityMode`：默认 `off`，`shadow` 记录建议，`select` 使用新选择。

此前约 1% 试用的讨论基于“需要目标业务样本来比较备用线路”这一前提。用户将目标收窄为 client/server，并明确已有 server 本地 healthcheck 后，该前提不再成立。此处撤下业务试用与额外心跳设计，不把业务比例转成探测预算。

## 3. 主动探测、被动统计与保活

| 来源 | 职责 | 使用边界 |
| --- | --- | --- |
| 现有 observatory healthcheck | 按时间检查每个候选，获得 client/server 响应、失败与恢复证据 | 使用实际 server 本地返回路径；候选闲置时仍继续探测 |
| 正常流量所在 QUIC 连接 | 观察实际传输中的 RTT、波动、loss 和可归因故障 | 协议栈随数据/ACK 更新统计，Xray 按物理连接定期读取；不等用户业务响应或长连接关闭 |
| QUIC keepalive | 维护已建立连接，空闲时提供协议活动 | 不独立记作一次完整 healthcheck 或新建连接成功 |

探测频率沿用 observatory 配置，与用户请求数无关。首版不新增故障确认调度器；若后续需要更快确认，再在既有调度上考虑合并、限频和退避。

healthcheck 建立代理逻辑流时仍可能复用已有 H3 连接。探测成功能验证此次实际经过的路径，但不能据此声明新物理连接也能建立。新建 QUIC 握手曾失败时，恢复证据必须包含新的成功握手，而非仅靠旧连接保活。

小流量 healthcheck 不能证明大流量时的容量、吞吐或拥塞上限。活动连接的被动统计能观察当前负载下的变化；闲置线路的重载表现保留为未知，首版不做带宽压测或人为业务分流。

## 4. 指标与连接归属

| 指标 | 口径 | 用途 |
| --- | --- | --- |
| `probe_elapsed_ms` | 现有本地 healthcheck 的响应耗时 | 主动基线；保留 GET/HEAD、复用/新建口径 |
| `probe_failure_rate` | 有效窗口内失败检查 / 已完成检查 | client/server 服务可靠性；不等于包丢失率 |
| `transport_connect_ms` | 客户端 H3 Dial 开始至 QUIC 握手完成 | 复用流不产生新拨号样本 |
| `transport_failure_rate` | 握手失败和已建立连接异常关闭分别统计 | 故障判断与可靠性成本；排除目标站点和客户端取消 |
| `transport_rtt_ms` | QUIC 平滑 RTT 及其 deviation | 直接传输对端的实际网络表现 |
| `quic_loss_pressure` | QUIC 近期判失变化与同期发送计数 | 客户端发送视角的传输压力辅助证据，不声称精确双向丢包率 |

原生统计按物理连接采样一次。一次连接故障影响 100 条逻辑流，不等于 100 次独立线路故障；同一连接的包计数不能因 mux/xmux 复用而重复累计。正常关闭、客户端取消、目标超时以及仅属配置/协议的拒绝要保留类别，不自动判为网络故障。

XHTTP 的 `stream-one` 没有 session ID；其他模式的 session ID 也不等于 QUIC 连接 ID。上传、下载可使用不同客户端、连接池和对端；原生统计必须带实际连接、方向、出站归属和配置代次。route tag 与实际拨号的 hop tag 分开保留，直接对端统计不能复制成每一跳的独立样本。

首版复用锁定版本 QUIC 的 `ConnectionStats()`；不增加依赖或启用全量 qlog。最小采集点是 `splithttp/dialer.go` 自定义 H3 Dial 回调取得 `*quic.Conn` 后，连接关闭时释放采样登记。后续重连使用新的身份和计数基点。以下时序不能混淆：

- `DialEarly` 返回不等于 QUIC 握手完成；成功握手须以库提供的完成信号为准。
- UDP 本地 socket 创建成功、上行载荷开始转发，都不能直接记作远端成功；旧通用成功事件须按实际阶段解释。
- `GotConn` 与 `OpenStream` 返回只代表 HTTP 客户端取得连接，不代表 healthcheck 回包。
- 外层 HTTP 200/上传确认只证明对应传输阶段；server 本地 healthcheck 的内层响应是另一条证据。
- 普通代理流首次收到目标数据，不是 client/server RTT；首版不需要为此改造 mux/splice 首包路径。
- 原生 RTT 只归属实际 QUIC 对端；不能未核对连接边界就当成每一跳或 server→目标的 RTT。

## 5. 聚合与数据有效性

复用 observatory 内的有界统计，按出站、配置代次、来源和测量口径聚合。无需数据库、目标域名分组或逐目标健康基线。

以下是实现采用的工程起点，状态规则已有自动测试，权重不视为实网最优值：

| 参数 | 建议起点 | 约束 |
| --- | --- | --- |
| 运行时统计窗口 | 60s，12 个 5s 桶 | 短期尖峰不能单独支配切换 |
| 运行时采样/质量评估 | 5s | 定时采样另加关闭前最终快照；计数差分避免重复，网络 I/O 不在选主锁内 |
| 连接阶段样本权重 | 至少 3 个非空桶，约 20 个样本获得完整样本权重 | 低流量时明确证据不足，不虚构成功 |
| loss 有效分母 | 至少 50 个发送包且覆盖 3 个非空桶 | 仍须处理延迟判失与迟到 ACK |
| 原生时延较早基线 | 10min，排除最近 60s | 排除有失败、loss、计数修正的桶；纯时延变慢仍可能逐步进入历史基线 |

主动 healthcheck 的有效期按真实的逐出站探测节奏计算，不能一律套用 60s。标准串行探测和 burst sampling 的覆盖周期不同；5s 重读一次旧探测不等于又完成一次成功。没有新有效证据时不增加胜场，实际探测节奏要能支持期望的切换速度。

每个来源保留更新时间、有效分母和置信度。读取 `ConnectionStats()` 本身不能把静态 RTT/loss 快照刷新为新测量；结合连接活动和计数有效性识别新证据。缺失、过期或不可判定的数据标记 unknown，不能填 0ms、0% 或合成 1ms 的健康样本。

`PacketsLost`/`BytesLost` 可被迟到 ACK 修正，不能无符号相减。loss 回落、连接重建、零分母和延迟判失须显式处理；无法有效归因的窗口保留 unknown，不把修正视为零丢包或巨大惩罚。客户端统计只提供本端发送方向的判失迹象，不直接测得精确下行丢包率。

healthcheck 请求单独标记来源，避免探测结果又作为普通业务成功重复记数。共享 QUIC 连接的包计数仍只采一次。新业务活动也不能延长旧探测的有效期。出站移除/替换后丢弃旧代次迟到信号。

新质量快照必须从各来源的原始结果聚合。旧 runtime overlay 会合并 Alive/Delay、更新时间，并可生成 synthetic 1ms；不能把该混合结果重新当作纯 healthcheck 输入。旧 overlay 可以保留给旧消费者，新 Champion 使用明确区分 probe、transport 和连接阶段的数据。

### 5.1 丢包处理与方向边界

QUIC 已负责丢失数据的重新发送与所选拥塞控制；Champion 不重复实现这些机制，也不重放业务请求。新增工作是读取实际承载连接的判失证据作为质量成本。因此，healthcheck 经重发后成功，并不等于该连接没有丢包压力。

首版按每个物理连接采集 `PacketsSent`、`BytesSent`、`PacketsLost`、`BytesLost` 与 RTT 快照，使用同窗口、同方向的有效数据估计近期压力。多个连接聚合时按有效发送量加权，不能简单平均每个连接的百分比；包数与字节数反映同一批事件，不能重复作为两份独立损失累计。

锁定版本 API 的限制决定该指标只能称为发送端判失压力：

- `PacketsSent` 包括 ACK-only、握手等控制包；`BytesSent` 包括重发字节。因此分母不是纯业务数据包或净业务字节。
- `PacketsLost`/`BytesLost` 是可修正的发送端判失，迟到 ACK 会使其回落；计数下降、重建或分母为零时，不能生成普通有效比率。
- 即使两个快照的 loss 净差非负，也可能同时发生新判失与旧判失修正；净差为零不证明中间完全没有判失或重发。只靠快照无法重建这些中间事件。
- 发送与判失时刻不同；不能把本窗口新判失全部当成本窗口新发包的最终丢失。计数对应不明确的区间保留 unknown/修正标记，不能做无符号差分或将负值截成“零丢包”。

前述至少 50 个发送包、3 个非空桶是有效性起点，不代表完整置信度。少量包中的一次判失不能直接代表稳定的百分比。还需结合新鲜度、跨桶分布与计数修正降低权重。一次尖峰过后，不能只因为它仍留在 60s 窗口中，就反复获得“持续丢包”的新胜场；新的统计桶也必须支持判失压力或相关性能劣化仍在持续。

| 采集位置 | 可见内容 | 当前方案边界 |
| --- | --- | --- |
| client 的 `ConnectionStats()` | client 发送的 QUIC 包是否被及时确认，以及本端判失 | 不包含 server 自己的发送判失计数 |
| server 的 `ConnectionStats()` | server 发送的 QUIC 包是否被及时确认，以及 server 本端判失 | 如需此视角，必须增加 server 采集、反馈和真实连接关联；当前尚未纳入实现 |
| 本地 healthcheck 与 RTT | client/server 往返响应和传输表现 | 下行问题可能间接表现为变慢或失败，但不能据此计算下行丢包百分比，也不能保证捕获所有下行劣化 |

发送端的判失还可能受返回 ACK 丢失、延迟与重排序影响。因此即使未来取得两端统计，也应标为两端发送判失视角，而非已经精确分离的物理上行/下行丢包率。首版仅采 client 时必须在日志与覆盖说明中写明这一限制；双端反馈属于尚需单独设计的能力，不能宣称现有草案已经完整覆盖。

## 6. 评分建议

先比较可用性与可靠性等级，再比较同级候选的综合成本。值越小越好；它是选路分数，不是一次请求的真实耗时。

```text
P = healthcheck_mean_ms + jitter_scale * healthcheck_deviation_ms
D = max(c_connect * connect_excess_ms,
        c_rtt * rtt_excess_ms)
F = failure_cost_ms * max(c_probe * healthcheck_failure_rate,
                          c_link * transport_failure_rate)
L = loss_cost_ms * c_loss * normalized_quic_loss_pressure
score_ms = P + D + max(F, L)
```

`P` 使用现有 server 本地 healthcheck 的原始均值/波动，不复用旧 Champion 已含失败惩罚的综合分数，否则会再次计入 `F`。运行时额外时延 `D` 只比较同口径的健康基线；冷启动无基线时记 unknown，不把普通业务首响应加入公式。失败与 loss 取最大成本，以减少同一故障被重复惩罚。该公式是待校准启发式，不宣称已消除所有指标相关性。

`failure_cost_ms = 2000ms`、`loss_cost_ms = 500ms` 和 QUIC 压力参考值 5% 仅为回放起点。压力比率、置信度和阈值必须有真实窗口支持；可靠性明显较差的线路不能靠低原始 RTT 获得 preferred 偏好。

对有效压力估计 `r`，可先试 `L = 500ms × c_loss × min(1, r / 5%)`。5% 是惩罚归一化参考，不是超过即判死的阈值，也不表示更低的压力被忽略。比如 `c_loss=1`、`D=F=0` 时，A 的 `P=80ms, r=3%` 得到 380ms，B 的 `P=140ms, r=0.2%` 得到 160ms，稳定的 B 可在持续证据满足后晋级；这些是公式算例，不是实测物理丢包率或已验证系数。

没有运行时包指标的候选仍有 healthcheck 基线，但未知不等于零 loss。共同 healthcheck 的质量优势，以及现任已经确认的 RTT/loss 劣化，均可作为切换证据；仅仅少采了指标不算优势。备用无需先有与现任相等的业务量，后续本地探测继续补证；只有闲置小流量样本时不宣称已验证其高负载能力。

## 7. 完整决策流程

本节描述首版决策逻辑；状态规则已有确定性测试，数值仍为工程初值，未作实网校准。观测、资格、排序与切换分别处理。

### 7.1 候选资格与数据状态

| 状态 | 含义与建议条件 | 自动选择行为 |
| --- | --- | --- |
| available | 近期有效的 client/server 可用证据，未确认持续可靠性劣化 | 正常比较 |
| suspect | 可归因的首次故障或持续劣化，仍有成功证据或正在确认 | 保留在比较中，增加对应成本；可靠性有持续负面证据时排在 available 之后 |
| unavailable | 初拟 10s 内连续两个独立、可归因的连接尝试失败，且同阶段没有更新成功；或已由连续有效探测确认的本地 healthcheck 路径故障 | 排除，跳过性能冷却让可用候选接管 |
| recovering | 已不可用线路重新出现成功，尚未满足恢复条件 | 继续探测，暂不作为正常挑战者 |

unknown 是证据状态，不是一次健康恢复：没有样本或过期时，不生成分数，也不能把已确认 unavailable 自动改成可用。只有 runtime RTT/loss 缺失而 healthcheck 新鲜时，线路仍可有正常候选资格。

首次失败可以进入 suspect 做确认，但不会单凭一次事件立即完成可靠性晋级切换。available 优先于有持续可靠性负面证据的 suspect；普通时延差和小样本噪声不被随意扩大成可靠性等级差。只有同级候选才用综合成本和 preferred 偏好排序。

loss 增加质量成本，持续证据可推动普通晋级；不单凭 loss 比率宣布 unavailable，也不额外启动确认探测。suspect/不可用判断来自独立阶段失败与原始 healthcheck。客户端取消、正常连接关闭、server→目标故障不进入独立线路失败计数。

不能从大量成功中挑出两次失败直接判死。较新的同阶段成功打断连续失败计数，但不清空滚动失败率；旧复用连接成功不能打断“新建连接持续失败”的证据。一个逻辑连接尝试内部最终已有成功分支时，失败分支只作诊断，不算完整尝试失败。

### 7.2 启动、观测中断与 fallback

| 条件 | 推荐行为 |
| --- | --- |
| 没有配置 observatory | 沿用现有无观测轮询兼容路径；显式 preferred 命中时从它开始，不宣称这是质量选择 |
| 已配置，但启动时所有候选尚无有效样本 | 暂用仍有效且未确认故障的原选择；没有原选择时用候选中的 preferred，否则按既有候选顺序启动；记录未验证，分数为 unknown |
| 已有足够的有效观测，但尚无经过验证的擂主 | 直接选择可靠性最好、综合成本最低的候选；preferred 只在接近时参与，不先强行驻留 preferred 若干轮 |
| 观测读取短暂失败 | 仍新鲜的上一份快照可继续使用；过期后保持未确认故障的现任作为临时选择，暂停性能晋级并记录观测错误 |
| 现任被移出候选集或明确 unavailable | 立即重新选可用候选，不等待普通晋级门槛或冷却 |
| 只有从未测得的候选和已知不可用候选 | 可临时选择前者并明确 unknown；不能把后者因数据过期恢复使用 |
| 全部候选明确不可用或仍在恢复验证 | 返回空选择，交给现有 Balancer 的 fallback；没有 fallback 时沿用当前错误处理 |

缺失状态在旧代码中会变成默认 500ms；新质量路径应改为显式 unknown。这是新模式的行为变化，off 仍保留原实现。显式 preferred 不在候选时，沿用当前“候选首项作为默认偏好”的约定，不能选出集合外的 tag。

人工 override 的现有外部入口优先于自动策略；候选选择本身报错时仍先按 Balancer 的既有 fallback 处理，不修改这一调用顺序。

### 7.3 每次质量评估如何确定挑战者

先在可比较候选中求 `best`：可靠性优先，同级选最低综合成本；相等时优先保留现任，否则采用稳定的候选顺序。当前擂主是否 preferred 不再决定其他候选能否挑战。

preferred 的接近条件已确认：

```text
S_preferred - S_best <= max(10ms, S_best * 5%)
```

还必须满足相同可靠性等级、证据新鲜且来源可比。普通质量晋级建议满足：

```text
S_current - S_candidate >= max(20ms, S_current * 15%)
```

判断顺序如下：

1. 现任无效/明确不可用时直接接管，不使用下面的性能门槛。
2. 查找有持续可靠性优势或达到普通质量改善门槛的候选。若存在，准备普通晋级；preferred 只有在接近 best 且自己也满足此次晋级条件时，才可代替 best 成为挑战者。
3. 若 preferred 接近 best，但没有达到对现任的普通晋级门槛，不得因此挡住已满足晋级条件的 best。
4. 没有普通晋级者时，若 preferred 接近 best、同级且不是现任，才进入较慢的“接近回切”路径。
5. 都不满足则保留现任。preferred 本身明显更好时走普通晋级，不因其身份额外等待回切轮数。

缺少被动指标不会得到“零 loss 奖励”，也不要求备用线路先产生同等业务量。共同 healthcheck 基线仍可比较；现任出现有充分证据的实际 RTT/loss 劣化时，新鲜且稳定的备用 healthcheck 可提供接管资格。仅仅指标缺失而无明确质量优势时不计胜场；切换后继续观测新线路的实际表现，不宣称小流量探测证明了重载能力。

### 7.4 三类切换与证据计票

| 类型 | 条件 | 推荐等待方式 |
| --- | --- | --- |
| 故障接管 | 现任明确不可用、被移除或失去必要的候选资格 | 在下一次新流选择时立即接管，跳过性能等待和冷却 |
| 普通晋级 | 持续可靠性优势，或同级且超过质量改善门槛 | 4 个有效的 5s 统计桶，通常约 20s 证据；性能切换最小间隔初拟 30s |
| preferred 接近回切 | 无应优先执行的普通晋级，preferred 同级且接近 best | 6 个有效桶，通常约 30s 证据，并满足性能冷却 |

可靠性升级可以不要求“再快 20ms/15%”，但在现任仍可用时仍要通过持续证据；单个失败不等于立即切换。冷启动首次形成有效选择与配置移除属于初始化/必要接管，不被性能冷却阻止。

运行时采样/评分以 5s 为周期。故障事件即时更新可用性，使下一次选择可绕过性能计票。选路热路径读取现成快照，同周期稳态先检查现任资格再快速返回，不重复构造评分表，也不发起同步网络探测。

每桶最多计一票，并且比较双方仍须新鲜，且存在与本次判断有关的新观测。空桶、原样重读和其他无关线路的新事件不加票。挑战者或切换类别变化时重新计数；优势消失/反转时清零，短暂无新样本暂停，过期后清零。冷却到期后也要用新的有效证据再次确认，不能直接兑现很早以前攒下的胜场。

实现同时要求票数与从首票起的持续时间：普通默认至少 20s，preferred 接近默认至少 30s，均受 30s 性能切换冷却约束。滚动窗口无需先填满；healthcheck 稀疏时不能靠重复读同一结果补票。超过 10s 没有评估会清除挑战记录，避免流量中断后兑现旧胜场。

决策与提交必须在同一策略临界区验证版本并更新：当前擂主、挑战者、类别、胜场、已处理证据和最近切换时刻。禁止过期快照覆盖新决定；日志在提交后输出，网络 I/O 不在此锁内。

### 7.5 恢复与场景验收

unavailable 后首次对应阶段成功只进入 recovering，3 个不同有效周期的成功才恢复资格。探测、握手和已建立连接故障分别累计；不能把不同阶段的一次失败相加成两次，也不能用目标业务完成或旧复用探测证明新握手成功。之后重新参加正常排名，不能直接恢复为擂主。

握手阶段不可用时，现有 healthcheck 使用 fresh 标记绕过 mux/xmux，每次建立私有物理连接，完成后释放该 H3 客户端；需要三个不同周期的新握手成功。已建立传输路径故障可由本地 healthcheck 往返验证恢复。调度保持原 observatory 节奏，没有新增确认请求、合并或退避系统。

以下分数是同级、新鲜、可比较样本的设计示例，不是实测结果：

| 场景 | 预期行为 |
| --- | --- |
| 当前 B=100，preferred A 恢复后=150 | A 恢复资格，B 保持擂主 |
| 当前 B=100，preferred A=108，且 B 是 best | 差 8ms 在 10ms 接近范围内；经过 6 个有效桶与冷却后可回切 A |
| 当前 B=150，preferred A=180，第三条 C=110 | C 比 B 改善 40ms，超过 22.5ms 门槛；连续满足后晋级 C |
| 当前 B=200，best C=170，preferred A=180 | C 达到 30ms 晋级门槛，A 虽接近 C 却只改善 20ms；先让 C 普通晋级，不能被 A 挡住 |
| B 只在一个桶中延迟升高，随后恢复 | 挑战证据中断，保留 B |
| B 的物理连接故障使 100 个子流失败 | 记一次独立连接故障，不能直接当 100 次独立失败 |
| 10s 内大量新连接成功，期间偶发两次建连失败 | 计入真实失败率；不能仅凭总失败数达到 2 就判 unavailable |
| B 已被确认不可用，C 健康 | 下一次新流选 C，已有业务连接不迁移、不重放 |
| 没有用户流量 | observatory 继续按时间探测；不会因没有 1% 预算而停止 |
| 同一快照被 10000 个请求读取 | 最多形成一次本周期判断，不增加 10000 次胜场 |

## 8. 最小实施边界与兼容

已实现的分层：

1. 复用现有 healthcheck 数据，补足来源、更新时间和阶段归因；核对实际预期响应，明确当前状态码判定边界。
2. 在 XHTTP/H3 物理连接所有者接入 QUIC 统计、握手/故障结果、生命周期和去重；聚合不依赖业务首响应或日志开关。
3. 在现有 observatory 聚合器内发布有界质量快照，Champion 接入全候选比较、preferred 接近规则和固定周期抑抖。
4. 配置提供 `qualityMode=off|shadow|select`，默认 off；可先 shadow 观察建议，再显式使用 select。

| 实际触及位置 | 变化 |
| --- | --- |
| `transport/internet/splithttp/dialer.go`、现有客户端/连接生命周期 | 登记实际 QUIC 连接，采样并在关闭时释放，区分新握手与复用 |
| `features/extension/outbound_quality.go` | 可选质量接口、来源、物理连接身份、阶段与只读快照 |
| `app/proxyman/outbound/handler.go`、`outbound.go`、传输内存配置 | handler 启动时注册，删除时注销质量归属；恢复探测绕过复用；旧通用业务事件保持独立 |
| `app/observatory`、`app/observatory/burst` | 复用主动结果，独立聚合运行时数据与时间有效性 |
| `app/router/strategy_champion*.go` | 综合评分、全候选挑战、快照计票和原子切换 |
| router 配置及生成文件、质量日志 | `qualityMode`、来源、样本更新时间、分数与切换理由 |

可选质量快照接口用于两个实际存在的 observatory 实现与 Champion 的边界，不新增 feature 注册或插件框架。新质量聚合仍只接收 `subjectSelector` 覆盖的出站；旧 `ObservationResult.Delay/HealthPing` 和 runtime overlay 供其他策略继续使用，不能被新评分意外改写。

原 access log 的 `delay` 继续表示 observatory 快照：当前部署的主动部分来自本地 healthcheck，但旧 runtime overlay 仍可能影响该字段，不能把它一律当作纯 probe 样本。新质量日志应分别标注 `probe`、`transport_rtt`、`loss_pressure`、`score`、状态、样本数、年龄和 reason；不能把总分打印成实测 delay，也不需要等待每条用户请求完成再补日志。日志级别关闭不影响聚合，不逐包打印。

切换日志解释旧/新出站、切换类别、可靠性、P/D/F/L 成本、改善门槛、有效胜场与冷却。证据记录更新时间及 healthcheck 有效截止时间，缺失时延显示 unknown。每周期 Debug 记录稳定/观察状态，切换使用 Warning（select）或 Info（shadow），不逐包打印。

采样器在首次启用后随 observatory 实例生存。热重载切回 off 停止新规则参与选择，但已有采样器直到实例关闭才退出；首版不增加消费者引用计数。

首版不要求 Linux TCP_INFO、KCP、目标业务对照组、额外 OPTIONS/echo 服务、服务端协议更新或真实业务试用。内核/传输扩展只在实际需求出现时另行处理。

## 9. 验证与验收

实施时验证核心可观察行为：

- 既有本地 healthcheck 正常往返可形成主动基线；公共默认 URL 不覆盖用户配置；响应状态、请求失败与预期匹配保持明确区别。
- 正常业务的目标站点变慢或断开不会污染 client/server 质量；实际 QUIC RTT/loss 或可归因故障能够更新快照，不等待业务流结束。
- 一个连接上的多条逻辑流不重复累计包数/独立故障；复用没有伪造的拨号样本；握手未完成不能记为成功。
- loss 回落、零分母、静态快照、重连和出站替换不产生溢出、假零丢包或旧数据污染。
- 闲置候选继续接受按时间调度的 healthcheck；用户请求量为零时也不丢失探测；不存在新增业务分流或重复请求。
- 第三条最佳候选能挑战非 preferred 擂主；preferred 恢复但明显较差时不回切，只有同级且接近时才获得偏好。
- 同一快照的并发读取不加速计票；故障跳过性能冷却，unknown、全不可用、override 和 fallback 语义明确。
- off 保持既有行为，shadow 只记录建议；采样成本有界，既有 mux/splice 快速路径无需因目标首包观测而被禁用。

验证包含确定性事件/时钟回放、相关 package 聚焦和 race、16 候选稳态选择微基准，以及真实 loopback H3 握手/HTTP 往返。测试 server 使用固定标准 TLS 证书；首次使用仓库动态证书配置触发了既有证书刷新 race，已隔离记录在 `DEBUG.md`，本次不修复该服务端路径。最终命令和结果见 `tasks/todo.md`。未进行广域网延迟/丢包注入或生产验证。

## 10. 当前结论与待校准项

已明确：只比较 client/server；当前 healthcheck 在 server 本地返回；每 balancer 一个擂主；首版 XHTTP + H3；可靠性优先、preferred 接近阈值和恢复不自动回切；持续劣化按稳定优先处理。

首版实现为“现有 healthcheck + 实际 QUIC 连接统计 + Champion 全候选抑抖”，没有额外心跳、约 1% 业务试用或目标首响应采集。

采样周期、有效分母、权重与恢复阈值已落为工程初值，仍需按实际网络校准。实际 URL 和 server 路由没有读取；部署时继续使用既有本地 healthcheck，新质量记录要求 2xx。代码已在工作区实现，运行配置和线上实例未更改。

## 11. 依赖源码核验记录

2026-09-06 已按锁定提交只读核验，未安装新依赖：

- [apernet/quic-go 184d081eef3e: ConnectionStats 与 loss 修正](https://github.com/apernet/quic-go/blob/184d081eef3e/connection.go#L816)。
- 2026-09-07 再次按精确提交读取 [发送计数](https://github.com/apernet/quic-go/blob/184d081eef3e/internal/ackhandler/sent_packet_handler.go#L275) 与 [发送端判失](https://github.com/apernet/quic-go/blob/184d081eef3e/internal/ackhandler/sent_packet_handler.go#L827)，核对控制包分母和本端发送视角；没有安装依赖。
- Linux TCP_INFO 和 KCP 为前轮核查的后续扩展方向，当前方案不依赖它们。
