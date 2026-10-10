# deploy/

本目录存放安装、升级脚本与 systemd 单元模板：

| 文件 | 用途 |
| :--- | :--- |
| `install.sh` | 一键安装 Hub 或 Agent（Release 中同名资产） |
| `upgrade-hub.sh` | 一键升级 Hub，不修改 `hub.env`，失败自动回滚 |
| `upgrade-agent.sh` | 一键升级 Agent，确认重新连上 Hub，失败自动回滚 |
| `vps-commander-hub.service` | Hub systemd 单元模板 |
| `vps-commander-agent.service` | Agent systemd 单元模板 |

部署与运维说明见：

- [Hub 部署](../docs/deploy/hub.md)
- [Agent 部署](../docs/deploy/agent.md)
- [运维手册](../docs/operations.md)
