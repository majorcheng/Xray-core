# Champion 质量选路指标与技术边界

本文补充 [领域词汇表](../CONTEXT.md)，实现边界见 [ADR 0001](adr/0001-champion-hybrid-quality.md)。用户已明确当前 observatory URL 由 server 本地返回；以下新增质量指标用于 `qualityMode=shadow|select`，默认 off 保留旧语义。

| 术语 | 定义 | 测量或归因边界 |
| --- | --- | --- |
| outbound / 出站 | 由 tag 标识的出站配置 | 可包含链式代理；不能把 tag 当作物理连接 ID |
| balancer / 均衡组 | 选择候选并调用策略的实例 | 每组保留一个擂主；无需按目标站点建立评分组 |
| champion / 擂主 | 当前优先接收新流的出站 | 切换不迁移已有连接 |
| preferred | 用户指定的首选偏好 | 可靠性同级、证据新鲜且成本差不超过 `max(10ms, 最佳分数 × 5%)` 才适用；恢复不单独触发回切 |
| local healthcheck / 本地健康检查 | client 经候选到 server，再由 server 本地返回 | 当前部署路径由用户说明；仓库 blackhole `response.type = "health"` 支持本地 HTTP 204，未读取线上路由配置 |
| probe elapsed | observatory 的 healthcheck 往返耗时 | 含 client/server 传输、必要建链与 server 调度处理；不等于纯 QUIC RTT |
| probe success / 探测成功 | 新质量记录要求 HTTP 请求无错误且返回 2xx | 覆盖 server 本地 204；旧 standard/burst 对外结果仍保持 HTTP 无错误即成功 |
| probe failure rate | 同窗口内失败探测 / 完成探测 | 服务可用性指标，不是网络包丢失率 |
| logical flow / 逻辑流 | 一次代理分派或 mux 子流 | 多条逻辑流可以复用一个 QUIC 连接 |
| physical connection / 物理连接 | 承载实际流量的 QUIC 连接 | RTT/loss 按连接采样和去重，不按逻辑流复制 |
| route tag / hop tag | 选中的路由出站 / 实际拨号跳点 | 原生统计归属直接传输对端；不能复制到所有跳点 |
| generation / 配置代次 | 出站启动并注册质量 reporter 时的实例身份 | 阻止旧连接迟到事件污染同名新配置；构造但未成功加入 manager 的 handler 不替换现有状态 |
| runtime feedback / 运行时反馈 | 正常流量产生的可归因连接阶段结果 | 本地取消、正常关闭、server 到目标失败不直接计为线路故障 |
| transport connect time | 客户端 H3 Dial 开始至确认 QUIC 握手完成的耗时 | `DialEarly` 返回不等于握手完成；复用流没有新的物理拨号样本 |
| transport RTT | QUIC 到直接对端的平滑往返时间 | 从实际承载流量连接的 `ConnectionStats()` 读取；不是目标业务响应时间 |
| jitter / 波动 | 同口径时延在时间上的变化 | healthcheck 标准差与 QUIC RTT deviation 保持独立 |
| QUIC loss pressure | 本端发送判失与发送计数形成的近期压力估计 | 发送计数含控制流量，loss 可被迟到 ACK 修正且净差可掩盖中间事件；不等于业务包或精确双向丢包率 |
| sender loss view / 发送端判失视角 | 一端对自己发出的包是否及时被确认所作的判定 | client 统计不含 server 发送判失；回程 ACK 丢失/延迟也会影响判断，两端视角需分别采集 |
| keepalive | 已有 QUIC 连接的协议保活 | 空闲时补充活动；不作为独立的 healthcheck 成功或新建连接成功 |
| first response / 业务首响应 | 普通代理流首次取得目标业务下行 | 混有目标处理与 server→目标路径，排除出本方案评分；首版无需新增此项采集 |
| confidence / 置信度 | 样本数、时间覆盖、新鲜度和归因形成的工程权重 | 不宣称为统计置信区间；少样本不能假装充分可靠 |
| coverage / 覆盖范围 | 实际取得指标的连接、方向和来源 | 小流量 healthcheck 无法证明重载吞吐或容量 |
| availability / 可用性状态 | available、suspect、unavailable、recovering | 与指标是否缺失分开；恢复验证未通过不能作为正常挑战者 |
| unknown / 未知 | 缺乏有效样本或能力，无法作出该项判断 | 不是零延迟、默认 500ms 或 down；也不能清除已确认故障 |
| snapshot epoch / 快照周期 | 5 秒统计桶及独立的更新版本号 | 一桶最多一票，需本次比较有关的新证据；重复读取、空桶和无关线路更新不加票 |
| score_ms / 毫秒成本 | 综合质量换算分数 | 不是实测 RTT、TTFB 或某次请求耗时 |
| hysteresis / 抑抖 | 改善门槛、持续证据与冷却共同限制性能切换 | 明确故障使用快速切换路径 |
| recovery evidence / 恢复证据 | 覆盖先前失败阶段的新成功结果 | 复用 healthcheck 成功不能证明新 QUIC 握手已恢复 |
| shadow mode | `qualityMode=shadow`，计算建议但保留既有实际选路 | 校准评分方向；不能充当实际切换后的网络验证 |
| XHTTP 模式 | `stream-one`、`stream-up`、`packet-up` | 上传/下载可分用连接；session ID 与物理连接不一一对应 |

当前实现复用现有 healthcheck 与 QUIC 保活，并采集实际连接的被动统计。首版不包含约 1% 业务试用、独立 OPTIONS 心跳或目标对照组；具体权重仍需实际网络校准。
