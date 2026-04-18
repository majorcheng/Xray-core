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

## 2026-04-18 fork 提交整理

- [x] 核对当前工作区改动与 fork 远端状态
- [x] 按 `01~05` 主题拆分详细中文 commit
- [x] 落盘本轮改动说明 md 文件
- [x] 处理本地维护文件的忽略规则并清理工作区
- [x] 推送整理后的提交到 fork
- [x] 确认 `patches/` 继续保持本地维护，不纳入 fork 分支
- [x] 回退误入库的 `patches/` 目录提交
