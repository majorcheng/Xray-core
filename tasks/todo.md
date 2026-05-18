# 任务清单

- [x] 只读核对 `patches/01_core_runtime_observability_reload.patch` 与当前 `HEAD` 的失配位置
- [x] 在隔离 worktree 中迁移 01 patch 到当前 `HEAD`
- [x] 修正 `app/dispatcher/default.go` 与 `main/run.go` 等失配点
- [x] 验证 01 patch 可重新 `git apply --check`
- [x] 运行 01 patch 的最小充分测试与编译校验
- [x] 回写 `patches/01_core_runtime_observability_reload.patch`
- [x] 在隔离 worktree 中顺序应用 `01~05`，定位 `02~05` 的实际失配与回归问题
- [x] 修正 `02_transport_http_xhttp.patch`
- [x] 修正 `03_proxy_vless_reverse.patch`
- [x] 修正 `04_security_reality_rand_cache.patch`
- [x] 修正 `05_feature_healthcheck_blackhole.patch`
- [x] 验证 `01~05` 顺序 apply 与定向测试全部通过


- [x] 为 observatory 增加运行时业务成功/失败覆盖层
- [x] 在 dispatcher 与 outbound 路径接入链路级业务事件上报
- [x] 为 standard observatory、burst observatory 与 router 策略补回归测试
- [x] 回写 `patches/01_core_runtime_observability_reload.patch` 并顺序校验 `01~05`

- [x] 修复 `app/router/router.go::ReloadRules` 在 replace 模式遗漏 `domainStrategy` 刷新
- [x] 为 routing reload 的 `domainStrategy` 热更新补回归测试
- [x] 回写 `patches/01_core_runtime_observability_reload.patch` 的 routing reload 修复 hunk

## Review 小结

- `patches/01~05` 已全部按当前 `HEAD` 重新整理并回写。
- 最终顺序校验结果：在干净 worktree 中 `01 -> 02 -> 03 -> 04 -> 05` 的 `git apply --check` 与实际 apply 全部通过。
- 新增的最小回归保护：`app/reverse/bridge_test.go`、`transport/internet/reality/reality_test.go`，以及 05 patch 对应的 `health` 测试补齐。
- 定向验证已通过：`common/log`、`app/router` champion、`app/router/command` reload 提取、`main` reload scope、`proxy/http`、`proxy/socks`、`app/reverse`、`transport/internet/reality`、`proxy/blackhole`、`infra/conf`。
- 已知非本轮新增问题仍保留：全量 `go test ./app/router` 依旧依赖仓库外部资源 `../../resources/geosite.dat`，因此本轮继续采用与补丁直接相关的定向测试口径。
- 2026-04-18：在隔离树为 observatory 增加运行时业务成功/失败覆盖层；dispatcher 通过 session 事件把真实拨号、mux 与 relay 建立期失败回写 observatory，业务成功可恢复，网站自身业务失败保持原语义。
- 新增回归测试覆盖 dispatcher 事件上报、standard/burst observatory overlay、router 各 observatory 策略过滤 dead 节点，以及 relay 首包成功检测。
- 2026-04-18：按用户确认只修复 `reload routing` 遗漏 `domainStrategy` 热更新；`app/router/router.go::ReloadRules` 的 replace 分支现已同步刷新 `r.domainStrategy`，并新增 `app/router/reload_rules_test.go` 覆盖 replace 与 append 两条语义。
- 2026-04-18：合入上游 `XTLS/Xray-core#5971`；`common/geodata/ip_matcher.go::(*IPSetFactory).createFrom` 现已区分空地址族与 `/0` 全量 CIDR，`common/geodata/ip_matcher_test.go` 新增 `TestIPMatcherFullCIDR4` 与 `TestIPMatcherFullCIDR6` 回归覆盖；定向 `go test` 子集通过，`./common/geodata` 全量用例继续依赖仓库外部 `geoip/geosite` 资源文件。

## 2026-04-18 fork 提交整理

- [x] 核对当前工作区改动与 fork 远端状态
- [x] 按 `01~05` 主题拆分详细中文 commit
- [x] 落盘本轮改动说明 md 文件
- [x] 处理本地维护文件的忽略规则并清理工作区
- [x] 推送整理后的提交到 fork
- [x] 确认 `patches/` 继续保持本地维护，不纳入 fork 分支
- [x] 回退误入库的 `patches/` 目录提交

## 2026-04-18 合入上游 PR 5971

- [x] 核对当前分支、remote、merge-base 与 PR 5971 提交边界
- [x] 评估 PR 5971 与本地 geodata 文件的冲突风险
- [x] 合入 PR 5971 到当前 main
- [x] 运行 geodata 受影响范围最小充分验证

## 2026-04-21 champion 抗抖动优化

- [x] 为 champion 增加可配置的抗抖动 strategy settings
- [x] 在 champion 选主中引入 health ping 抖动惩罚与更保守的 preferred 回切
- [x] 为 observatory 运行时反馈覆盖层增加失败/恢复抑抖
- [x] 补齐 champion 与 observatory 的定向回归测试
- [x] 完成最小充分验证并补充 review 小结

### Review 小结

- `champion` 现已支持独立 `strategy_settings`：`candidateObservationCount`、`preferredObservationCount`、`healthPingJitterScale`、`preferredMaxDelayGap`。
- `app/router/strategy_champion.go` 现按“不同观测快照上的连续胜场”累计晋级与回切，避免同一份 observatory 结果被高并发请求重复记分。
- `app/router/strategy_champion_support.go` 新增基于 `health_ping.average/deviation/fail` 的综合评分，抖动大、失败率高的线路会被自动惩罚。
- `app/observatory/runtime_feedback.go` 现采用冷启动首个成功即 up、稳定期 2 次失败判 down、down 后 7 次成功恢复的抑抖语义；standard 与 burst observatory 均已接入。
- 定向验证已通过：`timeout 60s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`timeout 60s go test ./app/observatory ./app/observatory/burst ./infra/conf`。
- 已知限制保持不变：`go test ./app/router` 全量仍受 `../../resources/geosite.dat` 缺失影响，因此本轮继续采用与 champion 变更直接相关的定向测试口径。

## 2026-04-21 champion review finding 修复

- [x] 复现并确认 runtime feedback failure streak 跨新探测累积
- [x] 修复 overlay 在较新 base probe 和 health ping 到达后的状态重置
- [x] 修复 champion partial settings 默认值合并语义
- [x] 补充 observatory 与 champion partial settings 回归测试
- [x] 运行 review 后的最小充分验证并补充结论

### Review 小结

- `app/observatory/runtime_feedback.go` 现已在较新的 base probe / health ping 覆盖旧 overlay 时重置运行时 streak，单次新失败从新的探测周期重新计数。
- `app/observatory/burst/burstobserver.go::ReportOutboundSignal` 现已把 `LastUpdateUnixNano` 传给 overlay，健康探测与业务反馈的新旧比较保持纳秒级精度。
- `app/router/strategy_champion_support.go::ChampionSettings.normalized` 现已把 `HealthPingJitterScale <= 0` 统一收口为默认值，partial settings 不会静默冲掉抖动惩罚。
- 新增回归测试覆盖 standard observer、burst observer 与 champion partial settings 三条 review 路径。
- 定向验证已通过：`timeout 60s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`timeout 60s go test ./app/observatory ./app/observatory/burst ./infra/conf`。

## 2026-04-23 observatory 未监控 tag 严格忽略 runtime feedback

- [x] 复核 standard / burst observatory 的 runtime feedback 入口与监控集合边界
- [x] 为未纳入 subject selector 的 tag 增加严格忽略逻辑
- [x] 补充 standard / burst observatory 对未监控 tag 忽略与监控内 tag 保留的回归测试
- [x] 运行 observatory 相关最小充分验证

### Review 小结

- `app/observatory/observer.go::(*Observer).ReportOutboundSignal` 与 `app/observatory/burst/burstobserver.go::(*Observer).ReportOutboundSignal` 现已先校验 tag 是否属于当前 `subject_selector` 命中的监控集合；未监控 tag 的业务成功/失败反馈会被直接忽略，不再 synthesize 回 `ObservationResult`。
- standard / burst observatory 当前会在每次 runtime feedback 到达时按最新 selector 快照刷新 `monitored`，不再只在空集合时刷新；因此动态新增的监控 tag 可立即接收反馈，已移除 tag 也会立即被拒绝。
- 新增回归测试覆盖三条边界：未监控 tag 必须忽略、监控内但尚未形成 base status 的 tag 仍可接收 runtime feedback、以及 `subject_selector` 命中集合在非空缓存状态下继续变化时也会被立即刷新。
- 定向验证已通过：`timeout 60s go test ./app/observatory ./app/observatory/burst -count=1`、`timeout 60s go test ./app/router -run "TestChampion|TestBalancingRuleBuildChampion" -count=1`。
- 额外尝试过 `timeout 60s go test ./app/router -run "TestChampion|TestBalancingRuleBuildChampion|TestSimpleBalancer" -count=1`；其中 `TestSimpleBalancer` 仍因测试上下文未注入 core instance 在当前仓库基线下失败，与本轮 observatory 改动无直接关系，因此未纳入本次验收口径。

## 2026-04-24 champion 默认首选擂主可配置

- [x] 为 champion 增加 `preferredTag` 配置字段并接通配置链路
- [x] 让 observatory 与无 observatory 的默认首选逻辑都优先使用 `preferredTag`
- [x] 补充 champion 与 router 配置解析的最小回归测试
- [x] 运行受影响范围的最小充分验证

### Review 小结

- `app/router/config.proto::StrategyChampionConfig`、`infra/conf/router_strategy.go::(*strategyChampionConfig).Build` 与 `app/router/config.go::(*BalancingRule).Build` 现已接通 `preferredTag`，只写该字段时也会生成 `strategy_settings`。
- `app/router/strategy_champion_support.go::ChampionSettings.normalized` 现会裁剪 `preferredTag` 首尾空白；`(*championObservation).preferred` 优先使用命中候选集的显式 `preferredTag`，缺失时退回原有首项语义。
- `app/router/strategy_champion.go::(*ChampionStrategy).pickRoundRobinFallback` 现已在无 observatory 且当前无既有 champion，或旧 champion 已不在当前候选集时，优先从 `preferredTag` 启动首轮回退，随后继续沿候选数组顺序轮转。
- 新增回归测试覆盖四条边界：显式 `preferredTag` 的 observatory 首选语义、`preferredTag` 缺失时的旧逻辑回退、无 observatory 时从 `preferredTag` 启动 round-robin fallback，以及 stale `lastTag` 遇到新候选集时重新从 `preferredTag` 启动。
- 定向验证已通过：`timeout 60s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion' -count=1`、`timeout 60s go test ./infra/conf -run 'TestRouterConfigChampionStrategy' -count=1`。

## 2026-04-24 preferredMaxDelayGap 只限制向上选择

- [x] 复核 preferred 回切条件并确认 `preferredMaxDelayGap` 误伤向下选择
- [x] 将 `preferredMaxDelayGap` 收口为仅限制 preferred 更慢时的回切
- [x] 补充 preferred 回切方向语义的最小回归测试
- [x] 运行 champion 相关定向验证

### Review 小结

- `app/router/strategy_champion_support.go::(*championObservation).preferredCanReclaim` 现不再用 `abs(anchorDelay-preferredDelay)` 双向限流；只有 `preferredDelay > anchorDelay` 且上行分差超过 `preferredMaxDelayGap` 时，才会阻止 preferred 回切。
- preferred 本身更快时，`preferredMaxDelayGap` 不再成为阻断条件；这与“低延迟不该被 gap 拦住”的新语义保持一致。
- 新增回归测试覆盖两条关键边界：preferred 更快但分差很大时仍允许回切，以及 preferred 更慢且上行分差过大时继续禁止回切。
- `docs/champion.md` 已同步更新 preferred 与 `preferredMaxDelayGap` 的配置语义，避免继续按旧的绝对差规则理解 champion。
- 定向验证已通过：`timeout 60s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion' -count=1`。

## 2026-05-18 跟进上游 `1bdb488` / `v26.5.9`

- [x] 拉取 `origin/main` 与官方 `XTLS/Xray-core` `main` 的最新引用
- [x] 核对目标提交 `1bdb488c9ec09ea51e6899697d5b7437f3cf6eb2` 与当前分叉状态
- [x] 只读评估上游 `v26.5.9` 与本地 `main` 的合流风险
- [ ] 在隔离 worktree 中从当前 `main` 创建同步分支
- [ ] 将 `upstream-temp/main` 合入同步分支，保留本地 fork 定制
- [ ] 处理可能出现的冲突，并保持配置重命名、DNS/finalmask 与本地 champion/observatory 改动共存
- [ ] 运行受影响范围的最小充分验证
- [ ] 验证通过后，将主工作树 `main` 快进到已验证同步分支
- [ ] 清理临时 worktree、同步分支与临时 upstream 引用
- [ ] 补充本轮 Review 小结
