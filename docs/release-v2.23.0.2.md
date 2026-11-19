# v2.23.0.2 发布说明

本版本包含 v2.23.0 合并后的 BPS 缓存计费修复。搜索、上下文压缩和工具纠错过程中，同一轮的渐进用量快照按累计值核算。触发纠错时保留原生成轮已经上报的用量，再加入纠错轮的用量。请求记录、报价和实际扣费使用同一份汇总。

完整发布包含五个主程序包（Linux、macOS 的 amd64 和 arm64，以及 Windows amd64）和以下两个 Linux 登录运行包。

- `sub2api-reauth_2.23.0.2_linux_amd64.tar.gz`
- `sub2api-reauth_2.23.0.2_linux_arm64.tar.gz`

使用完整发布流程，保持 `simple_release=false`，仓库变量 `SIMPLE_RELEASE` 也应关闭。主程序的 `-version` 和配套运行包内的 `release-source.json` 可用于核对应用版本与完整 Git 提交号。运行包 `manifest.json` 中的 `tosub2_commit` 标记其依赖的源码版本。
