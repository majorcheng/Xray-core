# 2026-04-18 patch set 同步说明

基线提交：`b4650360d6a05c2842d2c7157fb8cb864bba637a`（`v26.4.17`）  
目标仓库：`git@github.com:majorcheng/Xray-core.git`

## 提交拆分

### 1. `334ce345 feat(core): 回迁 observatory 反馈、champion 与运行时 reload`

范围：`01_core_runtime_observability_reload.patch`

主要内容：
- 增加 `champion` 负载策略，并让 `roundrobin`、`random`、`leastping`、`leastload` 统一消费 observatory 返回值。
- 在 standard observatory 与 burst observatory 中增加运行时业务事件 overlay；真实拨号失败、mux 失败、relay 建立期失败会即时压低 outbound 状态，拨号成功、relay 建立成功与后续健康探测成功可恢复状态。
- `app/dispatcher/default.go::routedDispatch` 与 `app/proxyman/outbound/handler.go::Dispatch` 接入链路级事件上报，站点业务层失败继续保留原语义。
- access log 增加 `delay`、`egress`、sniff 协议和目标域名输出。
- `main/reload.go` 与 `app/router/command/command.go` 增加运行时 reload 链路；`app/router/router.go::ReloadRules` 的 replace 分支同步刷新 `domainStrategy`。
- 新增回归测试覆盖 champion、observatory overlay、routing reload、dispatcher relay 感知。

验证：
- `timeout 60s go test ./app/dispatcher ./app/observatory ./app/observatory/burst ./app/router ./app/router/command ./common/log ./main -run 'Test(NewOutboundDispatchContext|Observer|BurstObserver|ChampionStrategy|BalancingRuleBuildChampion|RoundRobinStrategySkipsDeadCandidateFromObservation|RandomStrategySkipsDeadCandidateFromObservation|LeastPingStrategySkipsDeadCandidateFromObservation|LeastLoadStrategySkipsDeadCandidateFromObservation|RelayAware|ExtractRoutingConfigFromCore|ReloadRules|ParseReloadScopes)' -count=1`

### 2. `c28f50c9 feat(transport): 回迁 HTTP/XHTTP、SOCKS 与 Linux 原始目标修复`

范围：`02_transport_http_xhttp.patch`

主要内容：
- HTTP CONNECT 客户端缓存与复用路径重构。
- HTTP inbound keep-alive 判定统一，1xx 响应转发补边界。
- SOCKS client 拨号与协议编码路径增强。
- SplitHTTP 默认 xmux 参数回迁。
- Linux TCP/UDP 原始目标地址解析补齐长度检查与错误透传。

验证：
- `timeout 60s go test ./proxy/http ./proxy/socks ./transport/internet/tcp ./transport/internet/udp -count=1`

### 3. `3474cc9e feat(reverse): 回迁 VLESS reverse 上下文补全与桥接测试`

范围：`03_proxy_vless_reverse.patch`

主要内容：
- reverse bridge 转发时补全 inbound tag。
- VLESS inbound reverse 分支保持当前仓库约定的 reverse 用户 forward 能力，并完善上下文处理。
- 增加 bridge worker 定向测试。

验证：
- `timeout 60s go test ./app/reverse -run 'TestBridgeWorker' -count=1`

### 4. `bfaa5ea4 perf(reality): 回迁 ML-DSA 公钥缓存与随机路径边界修复`

范围：`04_security_reality_rand_cache.patch`

主要内容：
- 增加 ML-DSA 公钥解析缓存。
- 统一随机区间 helper，处理 `max <= min` 边界。
- `getPathLocked` 增加空集合保护。
- 增加 Reality 定向测试。

验证：
- `timeout 60s go test ./transport/internet/reality -run 'Test(GetCachedMldsaPublicKey|GetPathLockedEmptyMapReturnsRoot|RandRange)' -count=1`

### 5. `3baf32cf feat(blackhole): 回迁 health 响应类型与配置测试`

范围：`05_feature_healthcheck_blackhole.patch`

主要内容：
- 新增 blackhole `health` 响应类型，返回 HTTP 204。
- 同步 `infra/conf`、`proto` 与 `pb` 生成代码。
- 增加 runtime/build/config 侧回归测试。

验证：
- `timeout 60s go test ./proxy/blackhole ./infra/conf -run 'Test(BlackholeHealthResponse|HealthResponse|Config_HealthResponseBuild)' -count=1`

## patch 文件状态

当前 `patches/README.md` 以及 `patches/01~05` 已与上述提交同步。  
已在干净导出树基于 `b4650360` 重新执行：

- `git apply --check patches/01_core_runtime_observability_reload.patch`
- `git apply --check patches/02_transport_http_xhttp.patch`
- `git apply --check patches/03_proxy_vless_reverse.patch`
- `git apply --check patches/04_security_reality_rand_cache.patch`
- `git apply --check patches/05_feature_healthcheck_blackhole.patch`
- 顺序实际 `git apply` `01 -> 02 -> 03 -> 04 -> 05`

结果：全部通过。
