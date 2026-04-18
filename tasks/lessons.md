# Lessons

- 维护长期自用 patch 时，不要只看 `git apply --check`；还要在当前 `HEAD` 上实际应用后跑受影响范围测试，否则容易把“可打补丁”误当成“功能仍可用”。
- 当补丁跨越 generated 文件、CLI 命令入口和运行时接口时，优先在隔离 worktree 中迁移，避免直接污染主工作区。
- 业务失败信号只认 outbound 链路级失败；网站自身返回异常、业务错误页和已建链后的上游失败保持业务层语义，不写坏 observatory 健康状态。
- review 提到的行为边界先与用户确认：`VLESS reverse` 的 forward 权限与 `reload all` 仅覆盖 `routing/log` 属于当前仓库的既定语义，本轮只修用户确认的 `domainStrategy` 热更新问题。
