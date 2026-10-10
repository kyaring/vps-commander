# v1.1.33 Agent 升级评估

评估基线：`main=db83ea9`（v1.1.32），候选代码：`feat/oauth-sliding-refresh` 当前 HEAD。

## 结论

**本次不要求升级 Agent。** v1.1.33 候选分支的改动集中在 Hub 的 OAuth、审计、通知、存储和管理面板；`git diff db83ea9..HEAD -- cmd/agent internal/cluster` 为空，没有 Agent 二进制或集群协议改动。现有 Agent 与本次 Hub 功能兼容，升级 Agent 不会带来这些 Hub 功能的额外收益。

## 兼容性与版本可观测性

- Agent 当前源码握手上报 `ProtocolVersion="2"`、`AgentVersion="2.0"`；本次没有修改这两个字段或 Agent 的握手协议。
- Hub 的持久化 `devices` 表目前保存节点状态、OS、架构、心跳和资源画像，不持久化 `agent_version` / `protocol_version`。因此不能仅凭生产 SQLite 记录准确列出每个在线节点的实际二进制版本；本次不伪造逐节点版本结论。
- `go test ./cmd/agent ./internal/cluster` 已通过；完整 Go 测试也通过。

## 发布与升级建议

- 等 v1.1.33 正式发布后，再按需升级 Agent；不是本次发布的阻塞项。
- `deploy/upgrade-agent.sh` 只下载 GitHub `releases/latest`，会校验 SHA256、备份旧二进制、重启服务并等待 Hub 同步确认，失败会尝试回滚。
- 当前 v1.1.33 还只是本地未推送分支。现在运行升级脚本只会安装当前正式 Release，不会安装这个分支的候选代码；本次没有运行该脚本，也没有重启任何 Agent。
