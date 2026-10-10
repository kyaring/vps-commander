# 文档索引

适用版本：v1.1.30（2026-10-10）。

| 文档 | 读者 | 内容 |
| :--- | :--- | :--- |
| [../README.md](../README.md) | 所有人 | 概览与 5 分钟快速开始 |
| [architecture.md](architecture.md) | 开发 | 组件、双通道、Agent 协议、执行模型、MCP 实现 |
| [security.md](security.md) | 管理员 | 认证、命名令牌、scope、设备白名单、安全等级、审计、已知边界 |
| [mcp-clients.md](mcp-clients.md) | 接入方 | Streamable HTTP / SSE / REST / stdio 配置、常见错误、工具参数 |
| [api.md](api.md) | 接入方 | REST 接口、scope、状态码、指标 |
| [deploy/hub.md](deploy/hub.md) | 管理员 | Hub 安装、环境变量全表、反向代理 |
| [deploy/agent.md](deploy/agent.md) | 管理员 | Agent 安装、升级、卸载、离线排查 |
| [operations.md](operations.md) | 管理员 | 升级回滚、备份、日志、指标、令牌轮换、FAQ |
| [development.md](development.md) | 开发 | 构建、测试、验收门禁、发布流程、文档同步规则 |
| [loadtest.md](loadtest.md) | 开发 | 500 Agent / 50 MCP 压测（英文） |
| [roadmap.md](roadmap.md) | 所有人 | 已完成、进行中、待开发、已知遗留 |
| [../CHANGELOG.md](../CHANGELOG.md) | 所有人 | 版本变更记录 |

## 历史资料

[archive/](archive/) 保存设计稿、评审、事故与交接记录。它们反映写作当时的状态，可能与现行版本不一致，不作为当前行为依据。归档时已统一脱敏：域名替换为 `hub.example.com`，节点名替换为 `hub-host`、`node-a` 等占位名，本地路径替换为 `<repo>`。

## 维护规则

- 现行文档描述当前发布版本的真实行为；计划中的功能只写在 `roadmap.md`。
- 新增环境变量、接口、工具或 scope 时，同步更新对应文档（清单见 [development.md](development.md#6-文档同步规则)）。
- 不写入真实令牌、密码、域名、IP、节点名或个人路径；示例使用 `hub.example.com`、`node-01`、`<TOKEN>`。
- 历史资料只追加、不改写结论；如需更正，在现行文档或新的记录中说明。
