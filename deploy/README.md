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

## 本机构建与部署（恢复期）

> **安全边界：** 本机部署必须由 OPC 逐次明确批准后执行。开发任务和常规验收不得运行真实部署；生产路径只允许在审核阶段执行 `--dry-run`。本节脚本不会读取 `/etc/vps-commander/hub.env`。

### 1. 可复现构建

在干净的 Git 工作树中运行：

```sh
sh scripts/release-build.sh
# 或指定输出目录
sh scripts/release-build.sh --out /root/work/release/manual-check
```

脚本固定使用 Go `go1.24.4`，目标为 `linux/amd64`，默认输出到仓库外的 `/root/work/release/<完整提交 SHA 前 12 位>/`。输出包含 `vps-commander-hub`、`vps-commander-hub.sha256` 和 `build-info.txt`。工作树有任何未提交或未跟踪改动时会拒绝构建。构建产物应先核对 SHA-256 和 build info，再用于后续部署。

### 2. 本机部署（需逐次批准）

先确认目标二进制的 SHA-256：

```sh
sha256sum /root/work/release/<12位SHA>/vps-commander-hub
```

经 OPC 对本次部署明确批准后，才可执行：

```sh
sudo sh deploy/deploy-hub-local.sh \
  /root/work/release/<12位SHA>/vps-commander-hub \
  --sha256 <64位SHA256>
```

审核时对生产路径只运行无副作用的演练：

```sh
sudo sh deploy/deploy-hub-local.sh \
  /root/work/release/<12位SHA>/vps-commander-hub \
  --sha256 <64位SHA256> --dry-run
```

部署脚本默认安装目录 `/opt/vps-commander`、数据库 `/opt/vps-commander/data/commander.db`、服务 `vps-commander-hub`，健康检查地址为 `http://192.168.20.132:9521/healthz`，等待最多 30 秒。测试时可用 `VC_INSTALL_DIR`、`VC_DB`、`VC_BACKUP_ROOT`、`VC_SERVICE`、`VC_HEALTH_URL`、`VC_HEALTH_TIMEOUT` 和 `VC_SYSTEMCTL` 覆盖默认值。`--dry-run` 只验证输入 SHA 并打印计划，不创建备份、不写文件、不调用 systemctl。

### 3. 备份与回滚

每次实际部署前，脚本会在 `<VC_BACKUP_ROOT>/backup-pre-<新二进制 SHA 前 12 位>-<YYYYmmddHHMMSS>/` 保存旧二进制 `vps-commander-hub`，并通过 SQLite 在线备份命令 `.backup` 保存 `commander.db`；数据库备份失败时不会替换二进制。新二进制先写入 `vps-commander-hub.new`，再原子替换并重启服务。

若健康检查未返回 `{"status":"ok"}`，脚本会恢复旧二进制并重启：恢复后健康检查成功时输出 `ROLLBACK OK` 并退出码 1；恢复仍不健康时输出 `ROLLBACK FAILED` 并退出码 2。**数据库不会自动回滚**，因为二进制回滚不代表数据库 schema 或写入可以安全逆转。只有在确认停写窗口、备份文件和恢复影响，并获得 OPC 单独批准后，才可手动恢复数据库，例如：

```sh
# 先停止 Hub 并确认没有进程继续写入数据库；经 OPC 批准后执行：
sudo systemctl stop vps-commander-hub
sudo sqlite3 /opt/vps-commander/data/commander.db \
  ".restore '<备份目录>/commander.db'"
sudo systemctl start vps-commander-hub
```

恢复数据库前必须另行确认备份完整、当前数据库可丢弃，以及恢复后应用版本兼容性。不要把以上命令当作自动回滚步骤。

### 4. 旧升级脚本说明

`upgrade-hub.sh` 仍是从 GitHub Release 下载的升级路径，不适用于本机 `main` 构建部署。它使用的 `127.0.0.1:<port>/healthz` 健康检查地址不适用于新机 Hub 当前监听的 `192.168.20.132:9521`；本卡不修改该脚本。未经另行修复和审核，不应使用它对新机执行升级。
