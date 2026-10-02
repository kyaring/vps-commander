# VPS-Commander 生产部署与运维

> 基线：2026-10-02

## 1. 拓扑
Internet → TLS Reverse Proxy → vpstool.kory.kdns.fr → 127.0.0.1:9521 → Hub。
Hub → Local Executor 或 Agent WSS。
Hub 不直接公网监听；Agent 不需要公网入站管理端口。

## 2. Hub
binary：/opt/vps-commander/vps-commander-hub
env：/etc/vps-commander/hub.env
db：/opt/vps-commander/data/commander.db
systemd：vps-commander-hub.service
listen：127.0.0.1:9521

## 3. Agent
binary：/opt/vps-commander/vps-commander-agent
env：/etc/vps-commander/agent.env
必须包含 VPS_COMMANDER_HUB_WS_URL、VPS_COMMANDER_AGENT_NAME、VPS_COMMANDER_AGENT_TOKEN。
env 文件必须 0600。

## 4. Credential
每个 Agent 使用独立 token；Hub SQLite 只保存 token hash。
生产不依赖 Cluster Secret fallback。
v2.0 代码仍兼容旧 -token 参数；v2.1 要求 secret 不进入 argv/cmdline。

## 5. Reverse Proxy
必须支持 HTTPS、WebSocket Upgrade、/agent/ws、/mcp/*、/api/v1/*、/panel/*、/.well-known/*、/oauth/*。

## 6. Clean Release
禁止 dirty tree build。
git status --short；go test ./...；go test -race ./...；git diff --check；make clean；make linux-amd64；make linux-arm64。
发布证据必须包含 vcs.modified=false 和 vcs.revision=<release commit>。

## 7. Rolling
backup → Hub install/restart → Agent recovery → single-node gray → rolling Agent → MCP smoke → audit → acceptance。
任何断联或授权异常都停止。

## 8. 日常检查
systemctl status vps-commander-hub
journalctl -u vps-commander-hub -n 100
curl -fsS http://127.0.0.1:9521/healthz
/api/v1/devices、Web Panel、MCP list_devices。

## 9. 当前生产审计
2026-10-02 Bug Hunt 发现生产 Hub/Agent 曾 vcs.modified=true。
这说明现场二进制不能直接视为 HEAD 的可复现产物；下一次发布必须 clean rebuild 并重新部署。

## 10. 回滚
保留 Hub/Agent binary、systemd env、commander.db backup、release commit。
协议不兼容时先恢复 Hub，再滚动恢复 Agent。
