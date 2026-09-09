# 任务清单

## 2026-07-28 跟进上游 `5ca6f4b7` / `v26.7.28`

### 实施状态

- [x] 刷新 `origin/main` 与官方 `XTLS/Xray-core` `main` 引用
- [x] 核对工作区、目标提交、分叉状态和上游变更范围
- [x] 用 `git merge-tree` 预判冲突及配置文件拆分风险
- [x] 等待用户确认本方案及下列本地验证范围
- [x] 在隔离 worktree 中创建 `sync/upstream-20260728-v26728` 并合入 `upstream-temp/main`
- [x] 处理 `infra/conf/transport_internet.go` 冲突，采用上游拆分结构并迁移本地配置语义
- [x] 复核自动合并的四个本地/上游交叉文件
- [x] 增加一个聚焦回归测试并运行已授权的定向验证
- [x] 验证通过后将主工作树 `main` 快进到同步分支
- [x] 在主工作树复验、更新 Review 小结并清理临时 worktree/分支/ref

### 合流前证据与方案

- 合流前 `main`/`origin/main` 均为 `a4827c1cb867609658fcd3216a6bb7d17c137b93`，tracked 工作区干净；忽略项 `.codex-remote/`、`DEBUG.md`、测试证书和 `patches/` 保持不动。
- 官方目标为 `5ca6f4b7d4dc20a881d4330e498892697627ec0c`，精确标签 `v26.7.28`；merge-base 为 `45cf2898ab12e97a55dd8f1f3d78d903340bdc9e`，合流前双方独有提交数为 `26 / 35`。
- 上游从基线起改动 102 个文件。双方共同修改 5 个文件；预测合并仅在 `infra/conf/transport_internet.go` 出现文本冲突，其他 4 个为 `app/dispatcher/default.go`、`app/proxyman/outbound/handler.go`、`main/run.go`、`transport/internet/tls/config.go`。
- 冲突解决采用上游 `infra/conf/transport_internet.go` 的拆分后结构，不保留旧文件中已迁出的整段实现；将本地 TLS `allowInsecure` Build 语义迁移到新 `infra/conf/transport_security.go`，将 XHTTP/XMux 默认 `maxConcurrency=8..16`、`hMaxReusableSecs=2400..3200` 迁移到新 `infra/conf/transport_method.go`，其余采用上游 `v26.7.28` 实现。
- 自动合并的 4 个交叉文件逐项审阅，确保 stats API、root `env` 配置和 TLS CA pin/cipher suite 上游变化与本地 observatory/reload/`allowInsecure` 逻辑同时成立。
- 合流在 `/tmp/xray-core-upstream-sync-20260728-v26728` 隔离 worktree 完成；验证通过后主工作树仅执行 `git merge --ff-only sync/upstream-20260728-v26728`。本轮不 push，不修改 ignored 的 `patches/` 归档文件。

### 验证授权

- 用户确认前仅完成只读核验、远端 fetch 和本计划文档更新；merge、冲突处理、目标代码修改和测试均在确认本章节后执行。
- 用户确认本方案即授权：创建/删除上述临时 worktree、创建/删除同步分支、执行一次 `--no-ff` 本地 merge、解决已列冲突、增加一个聚焦测试、运行“建议验证”中的本地命令，以及验证通过后对当前 `main` 执行一次 `--ff-only`。
- 不包含：`push`、远端分支/标签变更、生产部署/运行态验证、全仓 `go test ./...`、重写 ignored 的 `patches/` 补丁归档；这些动作如确有需要再单独确认。

### 建议验证

- 在 `infra/conf/transport_test.go` 增加 `TestSplitHTTPConfigDefaultXmux`，直接断言默认 `maxConcurrency=8..16`、`maxConnections=0..0`、`hMaxReusableSecs=2400..3200`；既有 `TestTLSConfigAllowInsecure` 继续同时断言配置层和 runtime `InsecureSkipVerify`。
- `timeout 180s go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestSplitHTTPConfigDefaultXmux|TestRouterConfigChampionStrategy|TestHeaderCustom' -count=1`
- `timeout 180s go test ./app/dispatcher ./app/proxyman/outbound ./app/stats ./core ./main -count=1`
- `timeout 180s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion' -count=1`
- `timeout 180s go test ./app/observatory ./app/observatory/burst ./app/router/command -count=1`
- `timeout 180s go test ./transport/internet/splithttp ./transport/internet/finalmask/xmc -count=1`
- `timeout 180s go test ./transport/internet/tls -run 'TestCalculateCertHash|TestVerifyPeerLeafCert|TestVerifyPeerCACert|TestCertificateIssuing|TestExpiredCertificate|TestInsecureCertificates' -count=1`
- `timeout 180s go test ./proxy/http ./proxy/socks ./app/reverse ./transport/internet/reality ./proxy/blackhole -count=1`
- `timeout 180s go test ./proxy/freedom ./proxy/tun ./proxy/wireguard ./common/net -count=1`
- `gofmt` 仅格式化本轮手工编辑的 Go 文件；末尾运行 `git diff --check`、关键补丁符号检查和主工作树核心复验。

### 预计影响文件

- 官方 merge：上游 `45cf2898..5ca6f4b7` 涉及的 102 个文件，内容保持官方提交边界。
- 手工冲突/语义迁移：`infra/conf/transport_internet.go`、`infra/conf/transport_security.go`、`infra/conf/transport_method.go`。
- 聚焦回归：`infra/conf/transport_test.go`。
- 自动合并重点复核：`app/dispatcher/default.go`、`app/proxyman/outbound/handler.go`、`main/run.go`、`transport/internet/tls/config.go`。
- 任务记录：`tasks/todo.md`。

### Review 小结

- 本轮从本地 `a4827c1cb867609658fcd3216a6bb7d17c137b93` 合流官方 `XTLS/Xray-core` `5ca6f4b7d4dc20a881d4330e498892697627ec0c`，目标版本为 `v26.7.28`，生成双亲 merge commit `aefeeb79b93ecfdfca8dfe3dae5eab20a186a8a9`，标题为 `merge(upstream): 合入 XTLS/Xray-core v26.7.28`。
- 真实合流与预判一致，唯一文本冲突为 `infra/conf/transport_internet.go`。解决时采用上游拆分后的文件，不保留会产生重复定义的旧实现；TLS `allowInsecure` Build 语义迁移到 `infra/conf/transport_security.go`，XHTTP/XMux 默认 `maxConcurrency=8~16`、`hMaxReusableSecs=2400~3200` 迁移到 `infra/conf/transport_method.go`。
- 新增 `infra/conf/transport_test.go::TestSplitHTTPConfigDefaultXmux`，断言本地 XMux 默认范围和 `maxConnections=0~0`；既有 `TestTLSConfigAllowInsecure` 继续同时覆盖配置层 `AllowInsecure` 与 runtime `InsecureSkipVerify`。
- 自动合并的 `app/dispatcher/default.go`、`app/proxyman/outbound/handler.go`、`main/run.go`、`transport/internet/tls/config.go` 已按两个父提交复核：本地 observatory/relay feedback/reload/PID/`allowInsecure` 保留，上游 stats manager API、root `env` 说明、CA pin serverName 校验和 unsafe cipher suite 支持同时接纳。
- 本地关键补丁签名复核通过：`champion` strategy、standard/burst `ReportOutboundSignal`、routing `ReloadRules`、XHTTP `GenerateSessionID`、VLESS reverse、REALITY `mldsaKeyCache`、blackhole `HealthResponse` 均仍存在；`allow_insecure` proto/generated/runtime 全链路完整。
- 隔离 worktree 的计划内验证全部通过：`infra/conf` 聚焦测试；dispatcher/proxyman/stats/core/main；router champion；observatory/burst/router-command；splithttp/XMC；TLS 本地证书校验子集；HTTP/SOCKS/reverse/REALITY/blackhole；freedom/TUN/WireGuard/common-net。`proxy/freedom` 与 `proxy/wireguard` 无测试文件，但 package 编译通过。
- 主工作树快进后核心复验全部通过：`infra/conf` 聚焦测试、splithttp/XMC、router champion、observatory/burst、dispatcher/proxyman/stats/core/main；`git diff --check` 无错误。
- 主工作树通过 `git merge --ff-only sync/upstream-20260728-v26728` 快进到 `aefeeb79`；临时 worktree、同步分支和 `upstream-temp/main` ref 已清理。未触碰另一个既有 prunable worktree 记录、ignored 的 `.codex-remote/`、`DEBUG.md`、测试证书或 `patches/`。
- 未执行全仓 `go test ./...`、生产运行态验证或 `push`，这些动作不在本轮授权范围内。

## 2026-06-28 跟进上游 `45cf2898` / `v26.6.27`

- [x] 拉取 `origin/main` 与官方 `XTLS/Xray-core` `main` 的最新引用
- [x] 核对当前工作区、目标提交与分叉状态
- [x] 在隔离 worktree 中从当前 `main` 创建同步分支
- [x] 将 `upstream-temp/main` 合入同步分支
- [x] 处理 `infra/conf/transport_internet.go` 冲突并保留本地 `allowInsecure`、`champion`、observatory、reload、HTTP/XHTTP/SOCKS、VLESS reverse、REALITY、blackhole patch
- [x] 运行受影响范围的最小充分验证
- [x] 验证通过后，将主工作树 `main` 快进到已验证同步分支
- [x] 清理临时 worktree、同步分支与临时引用
- [x] 补充本轮 Review 小结

### Review 小结

- 本轮从本地 `4ac63fbc` 合流官方 `XTLS/Xray-core` `45cf2898ab12e97a55dd8f1f3d78d903340bdc9e`，目标版本为 `v26.6.27`，生成本地 merge commit `b9851659`，标题为 `merge(upstream): 合入 XTLS/Xray-core v26.6.27`。
- 合流在隔离 worktree `/tmp/xray-core-upstream-sync-20260628` 中完成；主工作树随后通过 `git merge --ff-only sync/upstream-20260628` 快进到已验证提交，并已清理同步 worktree、同步分支与 `upstream-temp/main` 临时引用。
- 本轮唯一手工冲突在 `infra/conf/transport_internet.go` 的 XHTTP/XMux 默认值。上游新增默认 `maxConnections = 6`，本地 `patches/02_transport_http_xhttp.patch` 明确保留 `maxConcurrency = 8~16` 与 `hMaxReusableSecs = 2400~3200`，因此按“保持自用 patch 可用”保留本地 XHTTP/XMux 调优，同时接纳上游其他变更。
- 上游本轮主要覆盖 metrics 生命周期与实例隔离、TUN 统计与跨平台处理、WireGuard 动态用户管理、Hysteria v2 配置简化、XHTTP upload queue 重构、geodata 下载 HTTPS/uTLS 调整，以及依赖 `github.com/cloudflare/circl v1.6.4`。
- 本地关键 patch 复核通过：TLS `allowInsecure` 配置与 runtime `InsecureSkipVerify` 语义仍存在；`champion` strategy、observatory runtime feedback、routing reload、HTTP/XHTTP/SOCKS、VLESS reverse、REALITY key cache、blackhole health response 均保留。
- 隔离 worktree 定向验证已通过：`go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestRouterConfigChampionStrategy|TestHeaderCustom'`、`go test ./transport/internet/splithttp`、`go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`go test ./app/observatory ./app/observatory/burst`、`go test ./app/metrics ./proxy/tun`、`go test ./proxy/http ./proxy/socks ./app/reverse ./transport/internet/reality ./proxy/blackhole`、`go test ./app/proxyman/outbound ./app/router/command ./main ./core`、`go test ./proxy/hysteria ./transport/internet/hysteria ./proxy/wireguard`、`go test ./app/dns/fakedns ./proxy/dns ./proxy/freedom`、`go test ./app/geodata ./transport/internet/finalmask/...`。
- 主工作树快进后复验通过：`go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestRouterConfigChampionStrategy|TestHeaderCustom'`、`go test ./transport/internet/splithttp`、`go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`go test ./app/observatory ./app/observatory/burst`、`go test ./app/metrics ./proxy/tun`。
- `git diff --check` 与 proto 生成头检查已通过；`core/config.pb.go`、`transport/internet/tls/config.pb.go`、`proxy/hysteria/config.pb.go`、`transport/internet/hysteria/config.pb.go`、`proxy/wireguard/config.pb.go` 头部均保持仓库生成版本 `protoc-gen-go v1.36.11`、`protoc v6.33.5`。

## 2026-06-23 跟进上游 `b99c3e56` / `v26.6.22`

- [x] 拉取 `origin/main` 与官方 `XTLS/Xray-core` `main` 的最新引用
- [x] 核对当前工作区、目标提交与分叉状态
- [x] 在隔离 worktree 中从当前 `main` 创建同步分支
- [x] 将 `upstream-temp/main` 合入同步分支
- [x] 处理冲突并保留本地 `allowInsecure`、`champion`、observatory、reload、HTTP/XHTTP/SOCKS、VLESS reverse、REALITY、blackhole patch
- [x] 运行受影响范围的最小充分验证
- [x] 验证通过后，将主工作树 `main` 快进到已验证同步分支
- [x] 清理临时 worktree、同步分支与临时引用
- [x] 补充本轮 Review 小结

### Review 小结

- 本轮从本地 `ef9c6bd2` 合流官方 `XTLS/Xray-core` `b99c3e56574fb0317608c49dd1dd9af816db7a9e`，目标版本为 `v26.6.22`，生成本地 merge commit `c0e8818b`，标题为 `merge(upstream): 合入 XTLS/Xray-core v26.6.22`。
- 合流在隔离 worktree `/tmp/xray-core-upstream-sync-20260623` 中完成；本次 `ort` 自动合并无手工冲突，随后主工作树通过 `git merge --ff-only sync/upstream-20260623` 快进到已验证提交，并已清理同步 worktree、同步分支与 `upstream-temp/main` 临时引用。
- 上游本轮主要覆盖版本号、`pion/stun/v3` 依赖更新、Linux TUN `XRAY_TUN_FD` 支持、fragment finalmask `lengths` / `delays`、以及 XHTTP server 在 `xPaddingObfsMode` 下的 `scStreamUpServerSecs` 处理。
- 本地关键 patch 复核通过：TLS `allowInsecure` 配置与 runtime `InsecureSkipVerify` 语义仍存在；`champion` strategy、observatory runtime feedback、routing reload、HTTP/XHTTP/SOCKS、VLESS reverse、REALITY key cache、blackhole health response 均保留。
- 隔离 worktree 定向验证已通过：`go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestRouterConfigChampionStrategy|TestHeaderCustom'`、`go test ./transport/internet/splithttp ./transport/internet/finalmask/fragment`、`go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`go test ./app/observatory ./app/observatory/burst`、`go test ./proxy/http ./proxy/socks ./app/reverse ./transport/internet/reality ./proxy/blackhole`、`go test ./app/proxyman/outbound ./app/router/command ./main`、`go test ./core ./proxy/tun ./transport/internet/finalmask/...`、`go test ./app/dns/fakedns`、`go test ./proxy/dns ./proxy/freedom`。
- 主工作树快进后复验通过：`go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestRouterConfigChampionStrategy|TestHeaderCustom'`、`go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`go test ./app/observatory ./app/observatory/burst`、`go test ./transport/internet/splithttp ./transport/internet/finalmask/fragment`。
- `git diff --check` 与 proto 生成头检查已通过；`core/config.pb.go`、`transport/internet/tls/config.pb.go`、`transport/internet/finalmask/fragment/config.pb.go` 头部均保持仓库生成版本 `protoc-gen-go v1.36.11`、`protoc v6.33.5`。

## 2026-06-20 跟进上游 `be8009c6` / `v26.6.1-24-gbe8009c6`

- [x] 拉取 `origin/main` 与官方 `XTLS/Xray-core` `main` 的最新引用
- [x] 核对当前工作区、目标提交与分叉状态
- [x] 在隔离 worktree 中从当前 `main` 创建同步分支
- [x] 将 `upstream-temp/main` 合入同步分支
- [x] 处理 `infra/conf/router_strategy.go`、`infra/conf/transport_internet.go`、`transport/internet/splithttp/config.go` 冲突
- [x] 保留并复核本地 `allowInsecure`、`champion`、observatory、reload、HTTP/XHTTP/SOCKS、VLESS reverse、REALITY、blackhole patch
- [x] 运行受影响范围的最小充分验证
- [x] 验证通过后，将主工作树 `main` 快进到已验证同步分支
- [x] 清理临时 worktree、同步分支与临时引用
- [x] 补充本轮 Review 小结

### Review 小结

- 本轮从本地 `d4016a26` 合流官方 `XTLS/Xray-core` `be8009c62509322682299bfbe969a62cee03f4d5`，生成本地 merge commit `e1953c23`，标题为 `merge(upstream): 合入 XTLS/Xray-core be8009c6`。
- 合流在隔离 worktree `/tmp/xray-core-upstream-sync-20260620` 中完成；主工作树随后通过 `git merge --ff-only sync/upstream-20260620` 快进到已验证提交，并已清理同步 worktree、同步分支与 `upstream-temp/main` 临时引用。
- 手工冲突集中在 `infra/conf/router_strategy.go`、`infra/conf/transport_internet.go`、`transport/internet/splithttp/config.go`；处理时采用上游新结构，同时保留本地 `champion` strategy、TLS `allowInsecure` 配置入口和 XHTTP session ID 相关新字段。
- 上游本轮移除了 runtime `tls.Config.AllowInsecure` 字段；已在 `transport/internet/tls/config.proto` 和生成文件中恢复 `allow_insecure = 1`，并在 `GetTLSConfig` 中恢复 `InsecureSkipVerify` 运行态语义，避免旧自用配置只通过解析但运行态失效。
- 适配了上游 API 变化：`app/router/observatory_overlay_test.go` 跟随 `LeastLoadStrategy.getNodes` 新签名；`app/observatory/burst/burstobserver_test.go` 为 `NewHealthPing` 传入 `context.Background()`，避免上游 `context.WithCancel(ctx)` 对 nil parent panic。
- 定向验证已通过：`go test ./transport/internet/splithttp`、`go test ./infra/conf -run 'TestTLSConfigAllowInsecure|TestRouterConfigChampionStrategy|TestHeaderCustom'`、`go test ./transport/internet/tls -run 'TestPinned|TestTLSConfigAllowInsecure'`、`go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion'`、`go test ./app/observatory ./app/observatory/burst`、`go test ./proxy/http ./proxy/socks ./app/reverse ./transport/internet/reality ./proxy/blackhole`、`go test ./app/proxyman/outbound ./app/router/command ./main`、`go test ./app/dns/fakedns`、`go test ./app/dns` 本地非 TCP 外部 DNS 子集、`go test ./proxy/dns ./proxy/freedom ./transport/internet/finalmask/...`。
- `git diff --check` 与 proto 生成头检查已通过；`transport/internet/tls/config.pb.go` 头部继续与仓库生成版本保持 `protoc-gen-go v1.36.11`、`protoc v6.33.5`。
- 已知限制：全量 `go test ./app/dns ./proxy/dns ./proxy/freedom ./transport/internet/finalmask/...` 中 `app/dns::TestTCPLocalNameServer` 访问外部 `8.8.8.8:53` 时返回 `EOF`，属于当前网络环境下的外部 TCP DNS 依赖失败；本轮保留失败证据，并用不依赖该外部 TCP DNS 的 DNS 子集完成回归验证。

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
- [x] 在隔离 worktree 中从当前 `main` 创建同步分支
- [x] 将 `upstream-temp/main` 合入同步分支，保留本地 fork 定制
- [x] 处理可能出现的冲突，并保持配置重命名、DNS/finalmask 与本地 champion/observatory 改动共存
- [x] 运行受影响范围的最小充分验证
- [x] 验证通过后，将主工作树 `main` 快进到已验证同步分支
- [x] 清理临时 worktree、同步分支与临时 upstream 引用
- [x] 补充本轮 Review 小结

### Review 小结

- 本轮从本地 `d245a1eb` 合流官方 `XTLS/Xray-core` `1bdb488c9ec09ea51e6899697d5b7437f3cf6eb2`，生成本地 merge commit `718fc6aa`，标题为 `merge(upstream): 合入 XTLS/Xray-core v26.5.9`。
- 合流在隔离 worktree `/tmp/xray-core-upstream-sync-20260518-v2659` 中完成，无手工冲突；随后主工作树通过 `git merge --ff-only sync/upstream-20260518-v2659` 快进到已验证提交。
- 上游本轮主要覆盖 DNS route probe 抽取到 `common/utils`、DNS outbound / Tunnel inbound 配置字段重命名、XHTTP stream-up 内存泄漏修复、freedom `finalRules` 的 `AsIs` IPv4 偏好、XDNS finalmask dialerProxy 边界，以及版本号更新到 `v26.5.9`。
- 首次 `timeout 180s go test ./app/dns ./proxy/dns ./infra/conf -count=1` 中，`./infra/conf` 因缺少 `resources/geoip.dat` 失败；按 `tasks/lessons.md` 记录的稳定地址补齐 `geoip.dat` 与 `geosite.dat` 后，`timeout 180s go test ./infra/conf -count=1` 通过。
- 定向验证已通过：`timeout 180s go test ./app/dns ./proxy/dns ./infra/conf -count=1` 中的 `./app/dns`、`./proxy/dns` 通过，`./infra/conf` 补资源后复测通过；`timeout 180s go test ./proxy/freedom ./transport/internet/finalmask/... -count=1`、`timeout 180s go test ./transport/internet/splithttp -count=1`、`timeout 180s go test ./app/router -run 'TestChampion|TestBalancingRuleBuildChampion' -count=1`、`timeout 180s go test ./app/observatory ./app/observatory/burst -count=1`、`timeout 180s go test ./common/utils ./core ./proxy/dokodemo ./proxy/vless/inbound -count=1` 均已通过。

## 2026-09-06 Champion 综合链路质量设计

- 范围：方案访谈与文档；不修改生产代码、运行配置或既有选路行为。
- 文档：`docs/adr/0001-champion-hybrid-quality.md`、`CONTEXT.md`、`docs/glossary.md`、本计划。
- 完成标准：明确主动探测、实际业务与传输层指标的口径、归因、聚合、评分、切换、兼容和验证范围；用户未确认的决策保持 Proposed。
- 验证：当前源码调用链、依赖接口与文档内部一致性；文档修改运行 `git diff --check`，不运行生产网络实验。

- [x] 读取 `grill-with-docs` 并核实关联技能可用性，刷新当前源码与 Git 状态
- [x] 核实业务首响应、连接复用和丢包指标的可测量边界
- [x] 写入 Proposed ADR 与领域术语表，列出建议参数和验收场景
- [x] 记录用户确认的 preferred 原则：可靠性和质量优先，仅质量接近时偏好首选，恢复不单独触发回切
- [x] 根据访谈收敛影响选路行为的关键决策
- [x] 检查文档与引用，记录本轮产物和待决事项

技能说明：初次草案时没有 `Skill` 工具，也未找到两个关联技能；后续回合 `grilling` 和 `domain-modeling` 已可用，主代理完整读取技能及 CONTEXT/ADR 格式说明后继续执行。Skill 工具仍未暴露，直接读取本地说明；按技能建立设计树、批次访谈和纯领域词汇表。

前阶段结果：Proposed ADR、`CONTEXT.md` 与指标口径文档已落盘；核实 TCP_INFO 与 QUIC ConnectionStats 可用接口。访谈已确认每个 balancer 一个擂主、首版 XHTTP + H3、稳定优先的 20-30s 质量切换、preferred 的可靠性优先原则与 `max(10ms, 最佳分数 × 5%)` 接近阈值。先前的约 1% 业务试用和目标首响应设计已由下节 client/server 范围澄清取代；具体系数、周期和启用方式仍待校准与评审。未修改生产代码或配置。

前阶段检查：已核查 XHTTP 在配置层映射到 `splithttp`，H3 的 ALPN 选择与 XmuxManager 复用边界；实际 OS 留到构建/集成时核验。文档空白检查通过，未执行行为测试、生产探测或网络故障注入；后续按下节修正后的范围推进。

## 2026-09-07 Champion 复用 server 本地 healthcheck

- 范围：依据用户澄清修正设计资料；用户只关心 client/server，现有 observatory URL 已由 server 本地直接返回。
- 影响文件：`CONTEXT.md`、`docs/glossary.md`、`docs/adr/0001-champion-hybrid-quality.md`、本计划。
- 完成标准：现有 healthcheck 进入主动基线，正常流量的 QUIC 统计提供补充；移除独立 OPTIONS 心跳、业务试用配额和目标业务对照组要求；保留 preferred 与抑抖共识。
- 验证范围：只读核查本地 healthcheck 能力和探测口径，检查文档一致性及空白；不执行生产探测或 Go 行为测试。

- [x] 记录用户提供的实际部署前提，撤销“固定 URL 必经外部目标”的推断
- [x] 核查 server 本地响应和 standard/burst 探测的实际代码边界
- [x] 同步领域词汇、指标口径与 ADR，移除因错误前提而增加的设计
- [x] 检查文档并记录本轮结果

本轮结果：`HealthResponse` 可在 server 本地直接返回 HTTP 204；实际部署为本地返回由用户确认，未读取线上配置。现有 observatory healthcheck 重新作为评分基线，正常流量所在 QUIC 连接提供 RTT/loss/故障补充，keepalive 作为保活；无需独立 OPTIONS 心跳、1% 业务试用或目标对照组。探测按时间调度，不能把用户流量百分比当作心跳预算。源码另表明 standard/burst 均未校验响应状态码，已记录“HTTP 可响应”和“healthcheck 匹配”的区别，没有修改旧判定契约。

本轮验证：`git diff --check` 通过，三份新增文档分别执行 `git diff --no-index --check` 均无空白错误输出；退出 1 表示文件差异。相关本地文档链接目标存在。未运行 Go 测试、线上探测或网络故障注入，未提交或推送；`.codegraph/` 保留原状，完整设计仍为 Proposed。

## 2026-09-07 Champion 完整逻辑评审

- 范围：说明并完善完整决策流程，继续设计阶段，不修改生产代码或配置。
- 影响文件：Champion ADR、必要的指标术语及本计划。
- 完成标准：从来源、快照、资格、排序到切换提交形成可复核流程；覆盖冷启动、未知、全不可用、恢复、preferred、第三候选和并发。
- 验证范围：当前源码及已有测试契约只读核验；文档场景推演与空白检查。

- [x] 对照当前 Champion、评分与 Balancer 入口确认现状
- [x] 明确三类切换、preferred 与普通挑战的先后顺序及缺样本处理
- [x] 补充完整流程、推荐参数与具体场景到 ADR
- [x] 核查文档一致性并完成完整逻辑说明

评审结果：ADR 第 7 节已明确资格/unknown、冷启动与 fallback、全候选排名、故障接管/普通晋级/preferred 接近回切、有效桶计票、恢复及具体场景。preferred 接近但未满足普通晋级条件时不阻挡 best 晋级；没有 runtime 样本不能获得零 loss 奖励，但备用也不需要与现任相等的业务量。新快照从原始来源聚合，避免旧 overlay 的 synthetic 1ms 和旧评分失败惩罚重复进入新模型。

故障判定补充：10s 内两次失败的初值限定为同阶段连续、独立、可归因且无更新成功的完整连接尝试；大量成功中的两次偶发失败只进入失败率。较新同阶段成功可打断连续计数，但不能清空滚动失败历史。

验证记录：已只读核验 Champion/Balance 主调用与已有无观测轮询、preferred 缺失、同快照计票测试；文档场景为逻辑推演，未执行 Go 测试或实网验证。文档 Git 空白检查通过，生产代码、运行配置和提交历史未改；阈值、采样节奏与启用方式仍为待校准/评审的提案。

## 2026-09-07 Champion 丢包处理说明

- 范围：解释并补充丢包采集、评分、抑抖和方向边界；影响 ADR、指标术语与本计划，不修改代码或配置。
- [x] 只读核验锁定 quic-go 提交的 ConnectionStats、发送计数与判失逻辑
- [x] 说明控制包分母、计数回落、净差抵消、小样本与单次尖峰的限制
- [x] 补充 loss 成本算例与客户端/服务端发送判失覆盖边界
- [x] 检查本轮文档空白与结果记录

结果：协议栈已有丢包恢复，新 Champion 的 loss 输入尚未实现。仅客户端快照能提供本端发送判失压力，不能承诺精确下行或双向丢包；server 采集/反馈尚需另行设计。5% 是拟议归一化参考而非判死线；healthcheck 重发后成功仍可有 loss 成本。未安装依赖、构建或执行网络故障实验。

验证：按 GitHub Raw 精确提交只读核验公开 ConnectionStats、SentPacket 和 detectLostPackets，主代理抽查关键源码；`git diff --check` 与新增文档独立空白检查通过。文档算例仅用于解释拟议权重，未执行 Go 或实际丢包实验。

## 2026-09-07 Champion 综合质量实现

- 授权：用户明确要求按已评审方案修改；完成客户端实现、配置、文档与相关本地验证，服务端统计回传和生产部署不在范围内。
- 影响：现有 observatory 与 burst、XHTTP/H3 物理连接生命周期、outbound 内存传输配置、Champion、router 配置及聚焦测试。
- 完成标准：healthcheck 与客户端 QUIC 指标独立聚合；全候选比较、preferred 接近规则、持续证据/故障快速路径、unknown 与恢复语义完整；提供 off/shadow/select 启用入口。
- 验证：相关 package 聚焦测试、必要的并发检查和配置生成一致性；不执行生产网络实验。

- [x] 检查 Git、当前实现边界和本地构建/生成条件
- [x] 实现分来源质量快照、出站代次、连接生命周期与被动统计
- [x] 接通 standard/burst 原始 healthcheck 结果与 H3 采样
- [x] 实现 Champion 新模式、可靠性/质量比较、抑抖、故障与日志
- [x] 同步配置、生成文件、文档与聚焦回归覆盖
- [x] 执行授权范围内的验证，修复本次改动导致的问题并完成交付记录

环境与授权：Go 1.27.1；用户随后明确“允许下载并完成生成与测试”。已下载 go.mod 锁定缺失模块，临时工具目录 `/tmp/xray-champion-tools.C6zZ70` 中使用 protoc 3.21.12 与缓存源码构建的 protoc-gen-go 1.36.11，仅生成 `app/router/config.pb.go`。未新增或升级项目依赖，`go.mod` / `go.sum` 无改动。缺少 unzip 时使用 Python 标准 zipfile 解压下载产物。

实现结果：

- 新增可选质量接口和共享有界聚合器，standard/burst 记录原始 2xx healthcheck；旧 overlay 与 access log delay 保留原语义。
- XHTTP/H3 按物理连接记录握手、RTT/波动、发送端判失与关闭；处理计数回落、迟到事件、重复关闭和出站代次。handler 启动时注册，Remove 时注销质量归属，不关闭已有业务连接。
- 探测、握手和已建立传输故障分阶段累计；恢复 3 个有效周期，新握手失败后的 healthcheck 绕过 mux/xmux 建立私有连接验证，完成后释放。
- `qualityMode=off|shadow|select` 默认 off。新决策支持全候选挑战、可靠性优先、preferred 接近规则、持续证据、性能冷却、即时故障接管及 unknown；切换日志保留提交时的有效票数并区分实测指标与评分。
- 补齐 `docs/champion.md` 使用片段，ADR 改为 Accepted，指标词汇同步。首版没有业务试用、额外心跳、server 统计回传或目标首响应采集。

最终验证（均实际执行）：

- `go test -mod=readonly ./app/observatory ./app/observatory/burst ./transport/internet/splithttp -count=1 -timeout=90s`：通过。
- `go test -mod=readonly ./app/router ./infra/conf -run 'TestChampion|TestBalancingRuleBuildChampion|TestRouterConfigChampion' -count=1 -timeout=60s`：通过。
- `go test -mod=readonly ./app/proxyman/outbound ./main -run 'TestOutboundQuality|TestInterfaces|TestRelay' -count=1 -timeout=90s`：通过；main 仅完成编译，该正则无 main 测试。
- `go test -mod=readonly -race ./app/observatory ./app/observatory/burst ./app/router ./app/proxyman/outbound ./transport/internet/splithttp -run 'TestQuality|TestChampionQuality|TestOutboundQuality|TestQUICQuality' -count=1 -timeout=90s`：最终通过。
- `BenchmarkChampionQualityPick`（16 个候选、同一快照、100ms 微基准）：发现并去掉稳态重复评分分配；本次 select 从约 9697ns / 27472B / 38 allocs 降至约 47.92ns / 0B / 0 allocs，off 约 1237ns / 2144B / 10 allocs。该基准使用固定观测替身，只衡量稳态选择开销，不代表网络或全代理吞吐。
- 所有本次 Go 文件通过 gofmt 检查；在临时输出目录重生成 router protobuf，与工作区生成文件 `cmp` 一致；Git 空白检查通过。

验证边界与已知问题：

- H3 检查使用真实 loopback QUIC 握手、HTTP 204 往返、物理连接统计及正常关闭；healthcheck 来源测试将 tagged 拨号替换为本机服务，验证 raw/overlay 隔离，不冒充完整代理部署。
- 首次 H3 race 检查发现既有服务端动态证书刷新与握手读取竞争（`transport/internet/tls/config.go:90,253`）。本次测试改用固定标准 TLS 证书后质量路径通过；未修改该服务端问题，详情见 `DEBUG.md`。
- 未执行广域网丢包注入、服务端发送统计反馈、生产部署或全仓资源相关回归；客户端 loss 仍是发送端判失压力，不能解释为精确双向丢包率。系数未作生产校准。
- 热重载切回 off 后已有采样器随 observatory 实例关闭才退出；较早时延基线排除失败/loss 桶，但纯时延劣化可能逐步进入历史基线。

Git：当前 `main`、`origin/main` 无跟踪分叉，基线 `1e8e0429cfc943f481349930c5776f0005fb00b7`。所有修改留在本地工作区，未提交/推送或更改运行配置；原有 `.codegraph/` 未改动。

## 2026-09-08 Champion debug vars 状态可视化

- 目标：在现有 `/debug/vars` 的 `observatory` 之外，提供每个 balancer 的 Champion 实际擂主、质量建议、候选评分、挑战票数、冷却和 loss 摘要。
- 范围：`app/router` 只读快照、`app/metrics` JSON 输出及聚焦测试；不改变选路、采样、配置或 wire protocol。
- 完成标准：无 router/旧模式也能稳定返回空 `champion`；select/shadow 能区分实际 current 与 quality 建议；时间、unknown、loss 发送端口径可读。
- 验证范围：状态快照单测、metrics `/debug/vars` 单测、相关 race；用户已明确授权本轮提交/推送。

实现状态：已新增 `ChampionStatus` 只读快照，通过现有 metrics handler 在 `/debug/vars` 增加顶层 `champion` 字段；不新增 API 端口、protobuf 或选路行为。快照同时保留 `current` 与 `quality_current`，区分已用于决策的版本和底层质量快照版本，候选指标缺失时输出 JSON `null`，并标明客户端发送端 loss 口径。

验证：`go test -mod=readonly ./app/router ./app/metrics -run 'TestRouterChampionStatus|TestChampionStatus|TestChampionQuality|TestChampionStrategy|TestBalancingRuleBuildChampion|TestMetrics' -count=1 -timeout=60s` 通过；`go test -mod=readonly -race ./app/router ./app/metrics -run 'TestRouterChampionStatus|TestChampionStatus|TestChampionQuality|TestMetrics' -count=1 -timeout=90s` 通过；`git diff --check` 与 gofmt 检查通过。原有 `.codegraph/` 未改动。

## 2026-09-08 Champion 配置示例与提交

- 授权：用户明确要求提供配置 sample，并 commit & push 本次改造。
- 范围：本次 Champion 源码、测试、生成文件、设计/使用文档与 JSON 配置片段；不包含原有 `.codegraph/`。
- 完成标准：sample 通过实际配置加载检查；提交仅包含本次任务文件，普通推送至既有 `origin/main`。

- [x] 核验工作区、分支、upstream 与实际 push 地址
- [x] 提供 `docs/examples/champion-quality.json`，补齐业务路由入口与合并说明
- [x] 使用实际 Xray 配置加载器验证 sample

本轮生产 Go 代码未改动，沿用前阶段已完成的聚焦回归、race 和生成一致性结果；新增验证仅针对 JSON sample。实际 push 地址为 `git@github.com:majorcheng/Xray-core.git`，本轮 commit/push 使用用户明确授权。

验证：`go run -mod=readonly ./main run -test -c docs/examples/champion-quality.json` 返回 `Configuration OK.`。该检查仅加载配置，不启动代理或验证示例地址的网络可达性；部署时应合入既有配置并替换 healthcheck URL 和出站标签。

交付方式：检查本次任务文件的暂存差异，创建聚焦提交并普通推送至 `origin/main`；最终提交哈希和远端核验结果通过交付回复及 Git 记录提供。

## 2026-09-08 跟进上游 `37ceb8b4` / `v26.9.8`

- 目标：将官方 `git@github.com:XTLS/Xray-core.git` `main` 的最新 10 个提交合入当前本地 `main`，保留本地 Champion、observatory、reload 和相关自用改造。
- 当前状态：工作区 tracked 文件干净；`main` 与 `origin/main` 同为 `5e669de4`，仅保留未跟踪 `.codegraph/`；当前分支与官方上游已分叉，本地独有 32 个提交、上游独有 10 个提交。
- 上游目标：`37ceb8b4b65ee919fb772a8034e572f76a6e87a2`（`Xray-core v26.9.8`）；共同基线为 `cd4ce973e9f6ef3a7acf9a7030927b4143f9ea47`（`WebSocket client: Avoid panic before real dialing in delayDialConn (#6544)`）。
- 上游变更范围：30 个文件，主要涉及 Go 1.27、REALITY 依赖、Freedom/TUN/Blackhole/XHTTP 修复，以及生成配置文件同步。
- 合流预判：`git merge-tree --write-tree HEAD FETCH_HEAD` 预测 3 个冲突路径：`infra/conf/blackhole.go` 内容冲突、`proxy/blackhole/config.go` 修改/删除冲突、`proxy/blackhole/config.pb.go` 内容冲突；其余路径可自动合并。

### 待执行

- [x] 用户确认后，在隔离 worktree 创建同步分支并合入上游 `FETCH_HEAD`
- [x] 处理 Blackhole 配置冲突，保留本地 health 响应语义并接纳上游配置结构
- [x] 复核自动合并交叉文件与生成文件一致性
- [x] 运行 Blackhole、Freedom、TUN、XHTTP、配置加载及本地 Champion 相关定向验证
- [x] 验证通过后将主工作树快进到同步分支，更新本节 Review 小结并清理临时引用

### 授权边界

- 本轮已完成只读核验、上游 fetch、冲突预判、实际 merge、冲突解决、代码/生成文件修改、测试和主分支快进；默认不 push 到 `origin`，`.codegraph/` 保持原状。

### Review 小结

- 本轮从本地 `5e669de4` 合流官方 `XTLS/Xray-core` `37ceb8b4b65ee919fb772a8034e572f76a6e87a2`（`Xray-core v26.9.8`），在隔离分支生成 merge commit `50f1a9dc657744e4cda7b959d519db60a68c266c`，主工作树随后通过 `git merge --ff-only` 快进到该提交。
- 上游本轮包含 Go 1.27 版本声明、REALITY 依赖更新、Freedom/TUN 兼容性修复、XHTTP `WaitReadCloser` 数据竞争修复、缓冲写入边界修复、Blackhole 自定义响应、Hysteria Unix socket 路径修复及 gRPC 依赖更新。
- Blackhole 三处冲突已解决：采用上游 `Response{type, custom_response_data}` 配置结构；将本地 `health` 迁移为 `response.type = "health"`，运行时继续直接返回 HTTP 204；删除旧 TypedMessage 配置文件与旧生成消息，避免与上游新 wire 结构重复。
- 自动合并交叉文件已复核：本地 Champion/observatory/relay feedback/reload、TLS `allowInsecure` 和 REALITY 缓存仍在；上游 Freedom/TUN、XHTTP、缓冲写入和配置生成变更已接纳。上游同时移除 outbound `proxySettings` 配置入口，改用 `streamSettings.sockopt.dialerProxy`，属于本轮上游既定行为变化。
- 隔离 worktree 定向测试全部通过：Blackhole/配置、Champion/router、observatory/burst、XHTTP、outbound/main/core、Freedom/TUN/common-net、HTTP/SOCKS/reverse/REALITY、TLS，以及相关 race 测试；Hysteria、VLESS outbound、testing scenarios 编译检查通过。
- 主工作树定向复验与 `git diff --check` 通过。一次合并包级测试触发既有资源依赖失败：`infra/conf::TestGeodataConfig` 缺少 `resources/geoip.dat`，`app/router::TestChinaSites` 缺少 `resources/geosite.dat`；本轮未下载资源，已用不依赖这些文件的定向测试完成验证。
- 临时同步 worktree `/tmp/xray-core-upstream-sync-20260908-v2698` 与同步分支已保留至本轮最终核验结束，随后清理；未 push、未修改生产配置或 `.codegraph/`。

## 2026-09-09 跟进上游 `52a412d9` / `v26.9.9`

- 目标：将官方 `git@github.com:XTLS/Xray-core.git` `main` 的最新 5 个提交合入当前本地 `main`，保留本地 Champion、observatory、reload、health 响应和其他自用改造。
- 当前状态：tracked 工作区干净；`main` 与 `origin/main` 同为 `54e6f7d0`，仅保留未跟踪 `.codegraph/`；本地相对官方上游独有 34 个提交，上游独有 5 个提交。
- 上游目标：`52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`（`Xray-core v26.9.9`）；共同基线为 `37ceb8b4b65ee919fb772a8034e572f76a6e87a2`（`Xray-core v26.9.8`）。
- 上游新增内容：`udpHop` 独立为新的 `finalmask/udphop`（含配置与连接实现），Freedom 在 `sockopt.dialerProxy` 下跳过域名解析和 `finalRules`，修复 VLESS 安全校验，重构 `infra/vformat` 并更新 Go 格式化依赖，版本号升至 `26.9.9`。
- 上游变更范围：41 个文件；双方共同修改 `infra/conf/transport_method.go` 与 `transport/internet/splithttp/dialer.go`。
- 合流预判：`git merge-tree --write-tree HEAD FETCH_HEAD` 仅预测 `transport/internet/splithttp/dialer.go` 内容冲突；该文件包含本地 Champion 质量探测/连接跟踪逻辑与上游移除旧 Hysteria `udphop` 接入的改动，需要手工保留本地质量逻辑并接纳新 finalmask 路径。

### 待执行

- [x] 用户确认后，在隔离 worktree 创建同步分支并合入上游 `FETCH_HEAD`
- [x] 解决 `transport/internet/splithttp/dialer.go` 冲突，保留质量探测/连接生命周期逻辑并移除旧 `quicParams.UdpHop` 接入
- [x] 复核 `infra/conf/transport_method.go` 默认值、QUIC 参数 protobuf 重排、新 `finalmask/udphop` 配置和 Freedom/VLESS 语义
- [x] 运行 finalmask/udphop、XHTTP、Champion/observatory、Freedom/TUN、配置和生成文件相关定向验证及必要 race 测试
- [x] 验证通过后将主工作树快进到同步分支，补充本节 Review 小结并清理临时引用

### 授权边界

- 本轮已完成只读核验、官方 fetch、分叉统计、变更范围审阅、`git merge-tree` 冲突预判、实际 merge、冲突解决、代码/生成文件修改、测试和主分支快进；本轮已获授权 push，未修改或提交 `.codegraph/`。

### Review 小结

- 本轮从本地 `54e6f7d0` 合流官方 `XTLS/Xray-core` `52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`（`Xray-core v26.9.9`），共同基线为 `37ceb8b4`，在隔离分支生成 merge commit `fd6c293ba52f934a768af36d4879622d95d53684`，主工作树随后通过 `git merge --ff-only` 快进到该提交。
- 上游本轮包含版本号更新到 `26.9.9`、新的 `finalmask/udphop`（配置、protobuf 和连接实现）、Freedom 在 `sockopt.dialerProxy` 下跳过域名解析及 `finalRules`、VLESS 安全校验修复、`infra/vformat` 重构和 Go 格式化依赖更新；同时移除旧 Hysteria `udphop` 实现。
- 唯一代码冲突为 `transport/internet/splithttp/dialer.go`。解决时采用上游移除旧 `quicParams.UdpHop` 的主体，保留本地 `extension` fresh probe、QUIC 质量开始/失败/连接跟踪、下载连接质量继承和 fresh probe 关闭清理逻辑；本地 XHTTP 默认 `maxConcurrency=8..16`、`hMaxReusableSecs=2400..3200` 继续保留。
- 新 `finalmask/udphop` 已通过最小配置构建检查，JSON `mode=intervalLocal`、`interval=5-5`、远端端口/IP 正确生成 `udphop.Config`；旧 `splithttp` `internet.UdpHop` 引用已全部清除。
- 上游新 `infra/vformat` 检查最初命中 8 个本地质量/Champion/HTTP 文件，已使用仓库自带格式化器只格式化这些文件；最终 `go run -mod=readonly ./infra/vformat/main.go -mode check -pwd ./` 通过。
- 隔离 worktree 定向测试通过：`splithttp`、`finalmask/...`、`infra/conf`、Freedom/TUN、VLESS outbound、vformat、core、crypto、pipe、observatory/burst、router、metrics、outbound、HTTP/SOCKS/reverse、REALITY、TLS；相关 race 测试也通过。
- 主工作树最终聚焦复验通过：配置、XHTTP、Champion/observatory、HTTP/SOCKS/reverse/REALITY、TLS、Freedom/TUN、场景编译和 vformat 检查均通过。宽泛测试中的 `app/router::TestChinaSites` 仍依赖缺失的 `resources/geosite.dat`；本轮未下载外部 geodata 资源。另一次 TLS 宽泛测试的 `TestECHDial` 依赖外部 ECH 服务并返回 `tls: server rejected ECH`，不作为本轮代码阻断。
- 临时同步 worktree `/tmp/xray-core-upstream-sync-20260909-v2699` 与同步分支已在最终复验后清理；本轮未修改生产配置，`.codegraph/` 保持未跟踪。
