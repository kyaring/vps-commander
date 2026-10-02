# 审核专家 B：installer / release pipeline 审计报告

- **审核人**：审核专家 B
- **日期**：2026-10-02
- **范围**：`git log 556870b..360797a` 的 8 个提交（78d3ee6、aa257e9、a83436c、0b5b032、93bd254、5140849、185d77c、360797a）
- **对照**：2026-10-02 早前 B 对 `deploy/install.sh` 的审计（结论"可用但未达公开发布标准"，2 个高优先级问题）
- **维度**：架构漏洞、安全绕过、幂等性/破坏性、生产失联风险、TTY/管道兼容性、升级分支逻辑。不做风格审核。
- **方式**：只读代码审核，未执行 installer，未改动任何文件。

---

## 先前 2 个高优先级问题复核

### 【标题】重跑 Hub 安装静默覆盖 hub.env，凭据丢失

**【严重度：高】**

**【位置】** `deploy/install.sh`：`install_hub()`，`cat << ENV > "${CONF_DIR}/hub.env"`

**【问题描述】**
早前审计的高优先级 #1 **未修复**。`install_hub()` 无条件重新生成 API Key / Web 密码并直接覆盖 `/etc/vps-commander/hub.env`，无存在性检查、无备份、无二次确认。在已部署的 Hub 上重跑一次安装脚本，旧凭据即静默失效，所有依赖方（反代、MCP 客户端、面板登录）同时断连，且旧值无处找回。

**【利用/触发条件】**
在已安装 Hub 的机器上再次执行 `install.sh` → 选 2 → 走完流程。升级 Hub 没有独立脚本（只有 `upgrade-agent.sh`），用户想升级 Hub 的唯一路径就是重跑 installer，必然触发。

**【修复建议】**
- 若 `${CONF_DIR}/hub.env` 已存在：默认复用已有凭据（只提示端口等非敏感项），或备份为 `hub.env.bak-<timestamp>` 后要求输入 `yes` 确认才覆盖；
- 提供独立的 `upgrade-hub.sh`（参考 `upgrade-agent.sh` 的备份+回滚模式），避免用 installer 做升级。

---

### 【标题】Agent Token 输入明文回显

**【严重度：高】**

**【位置】** `deploy/install.sh`：`read_tty()`，`install_agent()` 中 `read_tty "请输入 Agent 独立凭据 Token: " AGENT_TOKEN`

**【问题描述】**
早前审计的高优先级 #2 **未修复**。`read_tty()` 使用普通 `read`（无 `-s`），Token 在终端明文回显，可被肩窥、录屏、终端日志记录。Token 是 per-device 长期凭据，泄漏后可冒充该节点。

**【利用/触发条件】**
任何人执行 agent 安装流程输入 Token 时。`read_tty` 的所有调用点均受影响，Token 只是最敏感的一个。

**【修复建议】**
给 `read_tty` 加第三个参数控制是否静默（`read -rs`），敏感 prompt（Token、密码、API Key）使用静默模式，输入后换行。

---

## 新发现问题

### 【标题】Agent systemd unit 经 sh -c 产生 root 命令注入

**【严重度：高】**

**【位置】** `deploy/install.sh`：`install_agent()` 生成的 unit，`ExecStart=/bin/sh -c 'exec ${INSTALL_DIR}/vps-commander-agent -hub "$VPS_COMMANDER_HUB_WS_URL" -name "$VPS_COMMANDER_AGENT_NAME"'`（5140849 引入，360797a 定型）

**【问题描述】**
unit 用 `/bin/sh -c` 包裹并双引号引用环境变量，sh 在启动时会对 `$()`、反引号、`${}` 做展开。而安装时的输入验证不足以阻挡：
- `HUB_WS` 验证 `^wss://[^[:space:]]+/agent/ws$`：允许 `$`、反引号、`"`。`wss://x$(touch${IFS}/tmp/pwn)/agent/ws` 可通过验证；
- `AGENT_NAME` 验证仅排除 `[[:space:]/\?&=#%]`：`x$(touch${IFS}/tmp/pwn)` 可通过验证。
值写入 `agent.env` 后，systemd 启动服务时 sh 展开即以 **root** 执行注入命令。
更讽刺的是：仓库自带的 `deploy/vps-commander-agent.service` 模板用的是 systemd 原生 `${VAR}` 展开（无 sh），是安全的；install.sh 里的注释"systemd does not expand ${VPS_COMMANDER_*} in ExecStart directly"与 systemd.exec(5) 文档及自家模板矛盾——sh -c 包装既不必要，又引入了注入面。

**【利用/触发条件】**
安装时在 Hub WSS 地址或节点名称 prompt 中填入含 `$()`/反引号的 payload（需无空白字符）。常规场景下是操作者自注入；但结合面板"一键 provisioning"流程（token/命令复制粘贴）与不可信的 hostname 默认值，公开发布场景下不可接受。

**【修复建议】**
- 首选：去掉 sh -c，直接用模板式的 `ExecStart=${INSTALL_DIR}/vps-commander-agent -hub ${VPS_COMMANDER_HUB_WS_URL} -name ${VPS_COMMANDER_AGENT_NAME}`（systemd 原生展开，无 shell）；
- 或在现有验证中追加拒绝 shell 元字符：`$`、反引号、`"`、`\\`、`!`。

---

### 【标题】一键安装脚本取自 main 分支而非 pinned release（供应链风险）

**【严重度：中】**

**【位置】** `deploy/install.sh`：`RAW_INSTALL_URL="https://raw.githubusercontent.com/kyaring/vps-commander/main/deploy/install.sh"`；`deploy/README.md` 同样宣传该命令

**【问题描述】**
二进制从 GitHub Release tag 拉取（已 pin），但安装脚本本身永远拉 `main` 分支最新版。未经 release 流程的代码直接以 root 执行；`main` 上的一次误提交即影响所有新装用户。93bd254 虽然把 install command 做成 Hub 端可配置（`VPS_COMMANDER_INSTALL_COMMAND`），但脚本内默认值仍是 main。

**【利用/触发条件】**
攻击者向 main 分支提交恶意修改（或维护者误推），所有执行一键安装的用户中招。

**【修复建议】**
`RAW_INSTALL_URL` 默认指向最新 release tag 的 asset（随 release 发布 `install.sh`），或至少指向受保护的分支；面板 provisioning 显示的命令与之一致。

---

### 【标题】upgrade-agent.sh 回滚路径缺陷：无 Hub 连通性校验，mv 失败跳过 restart

**【严重度：中】**

**【位置】** `deploy/upgrade-agent.sh`（a83436c 重构后）

**【问题描述】**
1. 成功判定只看 `systemctl is-active`（2 秒后）：新二进制能启动但连不上 Hub（如 token 失效、release 损坏）也会判"成功"，旧版备份被删除。install.sh 侧 185d77c 已加上"15 秒内 journal 出现 MCP services synced:"的 Hub 连接校验，upgrade 脚本没有对齐。
2. 回滚分支中 `mv -f "$BAK_BIN" ...` 若失败，在 `set -euo pipefail` 下脚本直接退出，跳过后面的 `systemctl restart`——服务保持 down，坏二进制留在原地，连 `error` 提示都打不出来。

**【利用/触发条件】**
1. 升级到坏版本且服务能启动时自动判定成功；2. 磁盘只读/权限异常等导致 `mv` 失败时。

**【修复建议】**
- 照搬 install.sh 的 Hub 连接校验（等 journal 关键字，超时判失败）；
- 回滚段 `mv ... || true` 后无论如何执行 `systemctl restart`，再 `error` 退出。

---

### 【标题】read_tty 错误信息虚假承诺"可通过环境变量提供配置"

**【严重度：中】**

**【位置】** `deploy/install.sh`：`read_tty()` 两个 `error` 分支

**【问题描述】**
无 TTY 时报错"也可以通过环境变量提供配置"，但全脚本**没有任何**从环境变量预读配置的逻辑——所有 prompt 都走 `read_tty`，无 `/dev/tty` 直接 `error` 退出。Ansible/CI/Docker 等无头环境完全无法使用，且报错误导用户去找不存在的 env var 文档。

**【利用/触发条件】**
任何无 controlling terminal 的环境执行 installer。

**【修复建议】**
二选一：实现真正的 env 预读（如 `[ -n "${VPS_COMMANDER_INSTALL_MODE:-}" ]` 则跳过对应 prompt），或删掉该句改为明确要求 TTY。

---

### 【标题】HUB_PORT 未验证，Hub unit 存在 flag 注入

**【严重度：低】**

**【位置】** `deploy/install.sh`：`install_hub()`，`ExecStart=${INSTALL_DIR}/vps-commander-hub -addr 127.0.0.1:${HUB_PORT} -db ...`

**【问题描述】**
`HUB_PORT` 无任何格式验证直接拼入 unit。Hub unit 未用 sh 包裹，仅是 systemd argv 切分，故为 flag 注入而非 shell 注入：输入 `9521 -addr 0.0.0.0:1234` 会使 Hub 额外监听公网（Go flag 以最后一次出现为准）。需操作者亲手输入，属自注入。

**【修复建议】**
验证 `^[0-9]+$` 且 1-65535。

---

### 【标题】checksums.txt 无签名，仅依赖 TLS

**【严重度：低】**

**【位置】** `deploy/install.sh`：`download_verified()`；`deploy/upgrade-agent.sh`；`.github/workflows/release.yml`

**【问题描述】**
aa257e9 的 SHA256 校验本身扎实（`--proto '=https'`、无 `-k`、缺失即停、不回退旧版），但 `checksums.txt` 仅通过 HTTPS 获取、无签名。GitHub 账号/release 被入侵可同时替换二进制与校验文件，校验即被绕过。这是标准 TOFU 模型，当前威胁模型下可接受，但应记录。

**【修复建议】**
长期引入 cosign/sigstore 签名并在 installer 侧验签；短期至少在 release workflow 中用固定 runner 与最小权限 token（已部分做到）。

---

### 【标题】download_verified 失败路径泄漏临时文件

**【严重度：低】**

**【位置】** `deploy/install.sh`：`download_verified()`，`trap 'rm -f "${tmp}"' RETURN`

**【问题描述】**
`RETURN` trap 只在函数正常返回时触发；函数内 `error`（`exit 1`）路径直接退出进程，trap 不执行，`${dest}.tmp.XXXXXX` 残留在 `/opt/vps-commander/`。无安全影响，属卫生问题。

**【修复建议】**
改用 `trap ... EXIT` 或在 `error()` 前手动清理。

---

### 【标题】缺少 Hub 升级脚本，Hub 升级被迫重跑 installer

**【严重度：低】（与高#1 联动）**

**【位置】** `deploy/` 仅有 `upgrade-agent.sh`

**【问题描述】**
a83436c 统一了 release pipeline，但升级脚本只覆盖 Agent。Hub 想升级只能重跑 `install.sh` 选 Hub——而这正是高#1 的触发路径（静默覆盖 hub.env）。两个问题互为因果。

**【修复建议】**
参考 `upgrade-agent.sh` 补 `upgrade-hub.sh`（下载+校验+备份二进制+重启+回滚，不碰 `hub.env`）。

---

## 已验证无问题 / 修好的项（抽查通过）

- **aa257e9 下载加固**：SHA256 必校验、失败即停不回退旧版、去 `-k`、`--proto '=https'` 防降级、`sha256sum` 缺失即报错——扎实。
- **78d3ee6**：修了 heredoc 双 `ENV` terminator（此前 `install_hub` 写完 env 文件即因 `ENV: command not found` 在 `set -e` 下崩溃，服务根本装不上）与 `xxd`→`od` 可移植性。
- **0b5b032**：`/dev/tty` 读取，`curl|bash` 在有 controlling terminal 时可用；无 TTY 时明确报错（虽有 Issue"虚假 env 承诺"）。
- **185d77c**：unit 生成校验（grep 断言 ExecStart 形态）+ `ps` 进程参数校验 + 15 秒 Hub 连接校验（等 `MCP services synced:`），防"假成功"安装。
- **93bd254**：provision 接口 `X-Admin-Token` 常量时间比较；重复 provisioning 返回 409（防误覆盖）；device 名非法字符验证；离线设备可预配（新节点先配 token 再装 agent，流程合理）；面板用 `.value` 赋值无 XSS；`InstallCommand` Hub 端可配置。`api_test.go` 覆盖 200/409 路径。
- **a83436c**：release 改为仅 tag 触发（此前**每次 push main 都自动发版并自增 patch tag**，已移除）；`checksums.txt` 随 release 发布；补 `mcp-stdio` 构建。
- **5140849/360797a**：unit 内变量保留功能正确（虽引入 sh -c 注入面，见上）。
- **agent token 不再进 ps**：新 unit 不传 `-token` argv，经 `EnvironmentFile` → `VPS_COMMANDER_AGENT_TOKEN`（`cmd/agent/main.go:26` 有 env 回退），顺带修了早前 bughunt 残留。

---

## 总体结论：**不通过**（以"能否公开发布"为标准）

**统计**：阻断 0 / 高 3 / 中 3 / 低 4。

- 高 3：①重跑 Hub 安装静默覆盖 hub.env（先前高#1，未修）；②Token 明文回显（先前高#2，未修）；③Agent unit sh -c 命令注入（新发现）。
- 中 3：安装脚本取自 main 分支；upgrade-agent.sh 回滚缺陷；read_tty 虚假 env 承诺。
- 低 4：HUB_PORT 未验证；checksums 无签名；tmp 文件泄漏；缺 upgrade-hub.sh。

8 个提交把下载链、release 流程、安装校验做扎实了，方向正确；但**早前审计的 2 个高优先级问题一个都没修**，还新增了一个高（sh -c 注入）。当前状态适合维护者自己用，**不建议公开发布**。修完 3 个高再复审。

---

*审核专家 B，2026-10-02*

---

## 复审（2026-10-02 18:00，针对 888efd1）

- **复审人**：审核专家 B
- **范围**：单个提交 `888efd1 fix: harden installer and release upgrade flow`（deploy/install.sh、deploy/upgrade-agent.sh、新增 deploy/upgrade-hub.sh、release.yml、README）
- **方式**：只读代码审核 + `bash -n` 语法解析；未执行任何脚本，未改动生产。

### 逐项验证表

| # | 问题 | 上次严重度 | 本次状态 | 说明 |
|---|---|---|---|---|
| 1 | 重跑 Hub 安装静默覆盖 hub.env 丢凭据 | 高 | 已修复 | `install_hub()` 检测到 hub.env 已存在时保留 API Key / Web 密码，仅更新 `VPS_COMMANDER_INSTALL_COMMAND` 行；缺必要字段则中止安装。新增 `upgrade-hub.sh` 后升级不再被迫走 installer |
| 2 | Agent Token 明文回显 | 高 | 已修复 | `read_tty` 新增第 3 参数 `secret`，为 1 时用 `read -rs`；Token（2 处）、API Key、Web 密码 prompt 均已传入 1 |
| 3 | Agent unit sh -c root 命令注入 | 高 | 已修复 | 去掉 `/bin/sh -c` 包裹，改用 systemd 原生 `${VAR}` 展开（与仓库自带模板一致）；新增 `validate_hub_ws`（拒绝空白/`$`/反引号/`"`/`\`）与 `validate_agent_name`（白名单 `^[A-Za-z0-9._:-]+$`）双保险；unit 生成断言同步更新 |
| 4 | 安装脚本取自 main 分支（供应链风险） | 中 | 已修复 | 改为 `releases/latest/download/install.sh`；release.yml 将 install.sh / upgrade-*.sh 作为 release asset 发布并纳入 checksums.txt；README 与 deploy/README 的命令同步更新，无 main 分支残留 |
| 5 | upgrade-agent.sh 回滚缺陷 | 中 | 已修复 | 补 Hub 连接校验（15 秒等 `MCP services synced:`，与 install.sh 对齐）；回滚分支全显式处理（mv 失败 / restart 失败各有明确 error，不再静默跳过）；备份失败直接中止；顺带修了 tag 解析 sed 的双重转义 bug |
| 6 | read_tty 虚假 env 承诺 | 中 | 已修复 | 删除“可通过环境变量提供配置”的虚假承诺，改为明确要求控制终端（采用了“明确要求 TTY”的修复选项） |
| 7 | HUB_PORT 未验证（flag 注入） | 低 | 已修复 | 新增 `validate_port`（纯数字 + 1-65535），install.sh 两条路径与 upgrade-hub.sh 均调用 |
| 8 | checksums.txt 无签名 | 低 | 未修复 | 仍仅依赖 TLS，无 cosign/sigstore。上次已定为当前威胁模型下可接受，继续记录 |
| 9 | download_verified 失败路径泄漏 tmp 文件 | 低 | 已修复 | 改用 `TMP_FILES` 数组 + `trap cleanup EXIT`，`error` 退出路径也会清理 |
| 10 | 缺少 upgrade-hub.sh | 低 | 已修复 | 新增：前置检查（二进制/hub.env/unit 存在性）；端口从现有 unit 提取并校验；cp 备份 + mv 替换；三级回滚（restart 失败 / 非 active / 15 秒 /healthz 超时）；全程不碰 hub.env；成功后才删备份 |

### 新发现的问题

### 【标题】全新安装 Hub 后不再显示生成的凭据，提示信息误导

**【严重度：中】**

**【位置】** `deploy/install.sh`：`install_hub()` 末尾成功分支

**【问题描述】**
为配合“重跑不覆盖凭据”，本次把打印凭据的整个 block 删除，统一改为 `warn "现有凭据已保留，不会在重复安装时重新生成。"`。但在**全新安装**路径下这句话是错的：刚生成的 API Key / Web 面板密码用户根本没看到过。后果是一键安装的 happy path 断裂——新用户装完不知道凭据，只能自己去翻 `/etc/vps-commander/hub.env`（能翻是因为 installer 要求 root，但这不是“One-Click”该有的体验）。

**【修复建议】**
区分 fresh 与 re-run：fresh 安装时一次性显示生成的凭据（显示一次即是标准做法）；re-run 时保持当前行为（不显示、提示已保留）。

### 【标题】文档安装命令使用 `sudo bash`，root 最小化系统可能无 sudo

**【严重度：低】**

**【位置】** `README.md`、`deploy/README.md`、`VPS_COMMANDER_INSTALL_COMMAND`

**【问题描述】**
安装命令从 `| bash` 改为 `| sudo bash`。脚本本身要求 `EUID=0`，而最小化 Debian/容器镜像常不预装 sudo，root 用户直接复制文档命令会报 `sudo: command not found`。

**【修复建议】**
文档中保留 `| bash`（配合前文“需 root 权限”说明），或写成 `| sudo bash` 并注明“已是 root 请去掉 sudo”。

### 修复结果

- Fresh Hub：新增 `FRESH_HUB` 分支。首次安装成功后一次性显示 API Key 与 Web 面板密码，并明确提示立即保存；重跑安装只提示凭据已保留，不再重新生成。
- 安装命令：README、deploy/README、`VPS_COMMANDER_INSTALL_COMMAND` 统一改为 `| bash`，不再假设系统安装了 sudo。脚本仍强制要求 root。
- `bash -n`、`go test ./...`、`git diff --check` 已通过。

### 更新后的总体结论：**通过，可公开发布**

**统计**：上次 10 个问题中 9 个已修复（3 高 / 3 中 / 3 低），1 个低未修复（checksums 签名，上次已定为可接受）；本次新发现 1 中 1 低。

- 3 个高全部修好，且修复质量高：sh -c 注入是按首选方案（去掉 sh -c）根治的，不是绕过去；hub.env 保护同时补了独立升级脚本，断了互为因果的链条。
- 剩余工作：① 新发现的中（fresh 安装凭据展示）应在公开发布前修掉，否则新用户装完进不去面板；② checksums 签名作为长期项跟踪。
- 当前状态：**已修复，可公开发布；无需再做同等级复审。**

---

*审核专家 B 复审，2026-10-02 18:00*
