# Lessons

- 维护长期自用 patch 时，不要只看 `git apply --check`；还要在当前 `HEAD` 上实际应用后跑受影响范围测试，否则容易把“可打补丁”误当成“功能仍可用”。
- 当补丁跨越 generated 文件、CLI 命令入口和运行时接口时，优先在隔离 worktree 中迁移，避免直接污染主工作区。
- 业务失败信号只认 outbound 链路级失败；网站自身返回异常、业务错误页和已建链后的上游失败保持业务层语义，不写坏 observatory 健康状态。
- review 提到的行为边界先与用户确认：`VLESS reverse` 的 forward 权限与 `reload all` 仅覆盖 `routing/log` 属于当前仓库的既定语义，本轮只修用户确认的 `domainStrategy` 热更新问题。
- geodata 相关测试需要 `resources/geoip.dat` 与 `resources/geosite.dat` 时，优先使用用户确认的稳定下载地址：`https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat` 与 `https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat`。
- 计票去重只和上一次观测比较时，无版本号的观测必须在决策锁内读取，否则乱序提交会重复计票或撤销刚完成的切换；允许挑战者轮换时要按晋级目标单独计票，避免只在最后一轮领先的候选兑现其他候选的票。
- 计票证据要与本次劣势相关：运行时证据只要刷新就计票，会让与劣势无关的轻微 RTT 抬升按评估周期凑满票数；探测窗口只有少量样本时，要用最新样本确认劣势仍在，避免单个慢样本在整个有效期内持续计票。
- 计票证据游标要按票型分别推进，只消耗本票实际使用的观测；游标属于擂主任期，重新计数时不能一并清空，否则已计票的观测会被原样重读再计一票。只有状态真正变化时才更新“最近切换时间”这类事件时间。
