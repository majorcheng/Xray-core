# Champion 质量实现验证（2026-09-07）

## Observations

- 环境：Go 1.27.1，go.mod 锁定 quic-go `184d081eef3e`；仅本地聚焦验证。
- 草稿 `quicQualityFailure` 先过滤 `errors.Is(err, net.ErrClosed)`；该版本的 QUIC 超时、TransportError、ApplicationError、StatelessResetError 都包装 net.ErrClosed。
- 待复现：握手超时应报告故障，当前分类可能返回 false；普通本地关闭应保持非故障。

## Hypotheses

- H1（首要）：通用关闭过滤在具体 QUIC 分类之前吞掉故障。支持：下游 Unwrap 源码；反证：尚无。实验：直接把 HandshakeTimeoutError 传给生产分类函数。
- H2：依赖 API 与核验版本不一致。支持：之前未下载；反证：现在精确锁定模块已下载，源码包含相同 Unwrap。实验：聚焦测试编译并核对模块路径。
- H3：采集器生命周期丢弃事件。支持：reporter 代次存在待核查问题；反证：纯分类函数不依赖 reporter。实验：纯函数测试可隔离该因素。

## Experiments

- 添加五行以内的分类回归检查，先运行未修正实现；预计返回 false 会失败。
- `go test -mod=readonly ./transport/internet/splithttp -run '^TestQUICQualityTimeout$' -count=1` 实际失败：`QUIC handshake timeout was classified as normal close`。H1 已确认；H2 被精确模块编译排除；H3 不影响这个纯函数复现。

## Root Cause

通用 net.ErrClosed 过滤先于 QUIC 具体错误识别，而 QUIC 故障本身也包装该哨兵值。

## Fix

先识别 QUIC 应用/传输错误、超时、重置和版本协商失败，再过滤普通关闭；保留回归检查并扩展正常关闭边界。验证与交付状态见 `tasks/todo.md`。

## H3 测试服务端证书刷新 race

- 观察：首次质量聚焦 `-race` 中四个质量包通过，H3 测试在 `tls/config.go:90` 的 OCSP 更新与 `:253` 的服务端握手读取间报告 data race。
- H1（首要）：测试服务端使用仓库动态证书配置，触发既有 OCSP 刷新。支持：完整读写堆栈都在该路径；反证：无。实验：loopback 服务端只加载标准库固定证书，保留真实 H3 和客户端采集路径。
- H2：新 ConnectionStats 并发读取不安全。支持：测试新增该读取；反证：race 地址和读写堆栈均为服务端 TLS 证书，未涉及统计函数。
- H3：测试事件记录竞争。支持：事件跨协程；反证：通过 channel 同步，堆栈不在事件记录中。
- 处理边界：本任务不修改服务端证书刷新；静态测试证书无热更新需求，使用标准 TLS 证书配置。保留此已发现问题，不将该路径称为 race 已通过。
- 实验结果：固定证书下 `go test -race ./transport/internet/splithttp -run 'TestQUICQuality' -count=1 -timeout=30s` 通过，真实 H3 客户端采集和关闭流程完整执行；确认 H1。服务器动态证书刷新路径不在本次修复或通过声明内。
