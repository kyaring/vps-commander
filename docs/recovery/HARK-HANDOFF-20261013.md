# Hark 交接文档（2026-10-13 20:30，wjy5010u 时代）

接手人先读：本文件 → docs/recovery/STATE-20261013.md → docs/recovery/salvage/handoff/REVIEWER-HANDOFF-20261012.md（旧机版本，路径已失效，流程仍有效）→ docs/recovery/salvage/process/DEV-PROCESS.md。

## 1. 角色与铁律（不变）
- OPC = 用户，决定合并、部署、推送。生产变更每一步都要 OPC 明确批准。
- GPT、DeepSeek flash（DevTask）、workbuddy-2 是开发者；Hark 是独立审核和运维，不改业务代码、不自行合并部署。
- 模型只用 OPC 指定的（目前仅 global:deepseek-v4.1-flash），失败就如实报告，不换模型。
- 一个任务一个开发者；不给 flash 派与 GPT 重叠的任务。
- 卡、提交、日志、审核意见里不能出现任何令牌、密码、私钥。
- 例外：旧“快速迭代期不 push”规则造成了本次丢失；恢复期建议每段完成后经 OPC 批准推送 GitHub。

## 2. 当前环境
- 唯一节点 wjy5010u：Debian 13.7，i3-5010U 4 线程，7.7 GiB 内存，449 GiB 磁盘；局域网 192.168.20.132。
- Hub：/opt/vps-commander/vps-commander-hub，systemd vps-commander-hub.service，监听 192.168.20.132:9521，数据库 /opt/vps-commander/data/commander.db（新库），配置 /etc/vps-commander/hub.env（含 VPS_COMMANDER_PUBLIC_URL=https://vcs.king.nyc.mn，备份 hub.env.bak-20261013-oauth）。
- 运行代码 = GitHub main 891904a（v1.1.35），已用重新构建对比证实；“1.2.0” 只是 MCP serverInfo 字符串。
- 公网入口：https://vcs.king.nyc.mn/mcp（Streamable HTTP，只 POST，末尾无斜杠；initialize 后带 Mcp-Session-Id；握手过频会 429）。REST /api/v1/* 同域可用。
- 出网：开发工具走 SOCKS 192.168.20.189:1070（/etc/profile.d/dev-proxy.sh、git http.proxy、~/.ssh/config 走 ssh.github.com:443）。apt 直连。
- 工具：git 2.47.3，go1.25.14（/usr/local/go，GOTOOLCHAIN=auto，可自动下载 go1.24.4）。用 systemd-run 跑长任务时要显式设 HOME=/root、GOPATH=/root/go。
- 仓库：/root/work/vps-commander（891904a）、/root/work/dev-process（7a0f5dc）、/root/work/cloudotp-cloud（实为 Flutter 客户端 kyaring/cloudOTP，名字误导）。GitHub 密钥 /root/.ssh/id_ed25519_github（公钥是否已加到 GitHub 待确认）。
- 本次对比的临时构建在 /root/work/_cmp，可删。

## 3. 已完成的主机加固（10-13）
- 默认 multi-user.target，GNOME/gdm 停用；cups、cups-browsed、bluetooth、avahi、ModemManager、wpa_supplicant 已禁用并 mask。回退：systemctl set-default graphical.target，unmask 对应服务。
- nftables：入站默认拒绝；放行 lo、已建立、ICMP、DHCP、TCP 22；TCP 9521 仅 192.168.20.0/24。原配置 /etc/nftables.conf.orig。
- unattended-upgrades 开启（不自动重启）；journald 上限 500M、保留 2G。
- 未做：SSH 仍允许密码登录；commander.db 无异地备份；桌面软件包未卸载。

## 4. 进行中
1. ChatGPT OAuth：回调 https://chatgpt.com/connector/oauth/A13C459NoD5C 与 Hub 登记一致。报错 invalid OAuth client or redirect_uri 的最可能原因是 ChatGPT 仍填着 17:51 已禁用的 Chatgpt 客户端（vpc_client_6F02g1ENB6j2PKsq）。处理：ChatGPT 改用 gpt-2（vpc_client_oRNCfuP53mgXT7ly）及其 secret，没有 secret 就新建客户端；禁用 gpt-1。仍失败就查 journalctl -u vps-commander-hub 里 /oauth/authorize、/oauth/token 的 400。Hub 无 DCR/CIMD，注册 URL 留空。
2. 事件通知：OAuth scopes 不含 events:emit，MCP 无 emit_event。临时方案：用带 events:emit 的命名令牌调 REST POST /api/v1/events。代码改造等源码恢复后做（OPC 已明确暂不改代码）。
3. T-20261013-01（GPT，恢复开发环境）：已基本完成（工具、代理、两个公开仓库就位）；剩余 GitHub 公钥、私有仓库克隆、交付报告待 Hark 复核。
4. 新环境还没有：通知渠道（企业微信）、命名令牌体系、DevTask、看板。旧库数据全部丢失。

## 5. 待 OPC 决定
- wjyhk 能否恢复磁盘（决定是取回还是重写，见 STATE 文档第 6 节）。
- 是否推送 dev-process、vps-commander 恢复成果到 GitHub。
- SSH 关闭密码登录、commander.db 异地备份。
- 恢复顺序与开发者分配。

## 6. Hark 访问方式
- Hark 的令牌存于保险库（id 99db98f4-a9b7-40ba-ab58-989ca7bb869b），绑定主机 vcs.king.nyc.mn，经 MCP 调用（exec_command 的 timeout 字段，单次最长约 300 秒；更长的用 systemd-run 异步）。
- 旧 Hub vpstool.kory.kdns.fr 与旧令牌已失效。
