# vchub 算力节点架构梳理（本机 / 云 VM / 平台沙箱 / Simular Box）

> 日期：2026-11-12
> 性质：架构梳理文档（整合 deva 云端编译文档、Muse 宿主机规范、Hub 数据库与节点实测）
> 范围：vchub（vps-commander Hub）如何调度多种来源的算力，主要用于编译与测试
> 说明：文中不含任何令牌、密码、API Key。相关凭据问题见第九节。

---

## 一、一句话架构

**Hub 是调度层，各类算力是可替换的执行层，各 AI App 只是入口。**
算力来源分四类：本机常驻机、按需云 VM、AI 平台自带的沙箱（接成 Hub 节点）、Simular Box（桌面型沙箱）。

```
[入口]   GPT App / Claude App / Grok App / Muse App / Cue App
            │ MCP 工具调用
[调度层] vchub（Hub，本机 :9521）
            │ 节点主动出站连接 Hub；Hub 不主动连节点
[执行层] ├─ 本机 wjy5010u        常驻，弱 CPU，存源码和数据
         ├─ cue-01               Hub 节点，Cue 平台沙箱，强配置
         ├─ muse-01              Hub 节点，Muse 平台 VM，份额小且会被重建
         ├─ Freestyle VM `deva`  云 VM，走 API 直连（未接入 Hub）
         └─ Simular Box          云桌面，走自己的 MCP 工具（未接入 Hub）
```

## 二、分工原则

1. 常驻服务、源码仓库、持久数据只放本机。云端和沙箱一律视为临时算力。
2. 重 CPU 任务（全量 release 编译、全量测试）发到远端；增量编译和 `cargo check` 留本机。
3. 需要图形界面、浏览器渲染、人工接管的任务，交给 Simular Box。
4. 任何远端任务都按"随时会断"设计：后台运行、日志和完成标记写在持久目录、产物及时回传、断了能从标记处续跑。

## 三、算力清单总览

| 名称 | 类型 | 接入方式 | CPU / 内存 | 磁盘 | 生命周期 | 编译定位 |
|---|---|---|---|---|---|---|
| wjy5010u（本机） | 实体机 | Hub local | i3-5010U，2C4T / 7.7G | 449G | 常驻 | 增量、轻量、Go 项目 |
| cue-01 | 平台沙箱 | Hub 节点 | 8 核 / 24G | 54G（约 37G 可用） | 待验证（实测已连续运行约 31 小时） | **全量编译首选** |
| muse-01 | 平台 VM | Hub 节点 | 约 1.5 核份额（可见 4 vCPU）/ 6G 上限 | 家目录 150G，根盘仅 7.5G | **约 1–2 小时被整机重建** | 仅短中型增量构建 |
| Freestyle VM `deva` | 云 VM | API 直连 | 4 核 AMD / 8G | 32G | 空闲 5 分钟暂停，30 天自动删除 | 全量编译备选，实测 55 秒 |
| Simular Box `main` | 云桌面 | Simular 自己的 MCP 工具 | 2 核 Xeon / 3.8G | 20G | 会休眠，试用账号 | **不用于编译**，做 GUI 和浏览器 |
| hwsg | VPS（待确认） | 曾接过 Hub，当前未注册 | 未知 | 未知 | Muse 文档称"不会被平台替换" | 待确认后列入长任务备选 |

## 四、各节点详情

### 4.1 本机 wjy5010u

- 常驻服务：deva-headless、caddy-mcp、lazy-balancer、vps-commander-hub，合计约 240MB，都绑定本机端口或文件，不能外移。
- 全量 release 编译 deva-rust 需要 15–25 分钟，是整套方案要解决的瓶颈。
- deva-rust 的 `target/` 约 20G。源码仓库在 `/home/orca/projects/`，是唯一长期存储。

### 4.2 cue-01（Cue 平台沙箱，Hub 节点）

- 实测（2026-11-12）：8 核，内存 24G（可用约 21G），磁盘 54G（可用约 37G），root 身份。
- 已预装 cargo、rustc、go、node、gcc、git、curl、tar；没有 rsync，没有 Docker。
- 出站网络通：GitHub 和 crates.io 均返回 200，可以直接拉源码和依赖。
- 在 Hub 中的注册时间为 2026-11-12 04:36，此后一直在线。
- 未知项：沙箱会不会被回收、回收周期多长。验证前按临时节点处理。

### 4.3 muse-01（Muse 平台 VM，Hub 节点）

以下信息来自《Muse VM 宿主机使用规范》（本目录 `muse-vm-host-usage-guide.md`），以该规范为准。

- 容器型 VM，CPU 约 1.5 核共享，内存上限 6GiB，建议自留峰值不超过 5GiB。
- 磁盘分两块：根盘 7.5G 只能放系统件；家目录 `/home/hatch`（150G）才是工作区，所有文件放 `/home/hatch/workspace/`。
- **VM 会被平台整机重建，实测寿命约 1–2 小时**，进程当场终止，除家目录外一切清空。看门狗会自动恢复 agent。
- 出网只有 HTTP 代理 `hatch-egress-proxy:3128`，TLS 由沙箱 CA 重签，裸 TCP 直连无效。
- 面板和 `free` 显示的内存 90% 以上是宿主机口径，不代表真实用量，不要据此判断它不能用。
- 已预装 Go（在家目录卷上，重建不丢），用前执行 `. /home/hatch/workspace/toolchains/env.sh`。没有 Rust 工具链。
- 构建须知：后台运行（`setsid nohup`）、并行度约 `-j2`、缓存和产物放家目录、靠增量续跑；单个构建步骤超过 40 分钟基本不可行。
- 实测备注：从 Hub 直接 exec 时环境里没有代理变量，所以直连 GitHub 和 crates.io 会失败，这是环境问题，不是网络被封。要联网需显式设置代理变量。

### 4.4 Freestyle VM `deva`（云 VM，API 直连）

- 4 vCPU AMD EPYC / 8G / 32G，Ubuntu 24.04，有 Docker 29.x，已装 Rust（cargo 1.99.1）。
- 脚本 `cloud-build.sh`（`~/bin/` 与 `/var/lib/deva/bin/`），用法见 `deva/docs/cloud-build-usage.md`：`--test`、`--check`、`--sync-only`。
- 实测：deva-rust 全量 release 编译 54.84 秒，端到端约 1 分 40 秒，本机需 15–25 分钟。
- 计费：Free 档 200 vCPU 小时/月，单次约 0.11 vCPU 小时，约 1800 次/月。
- 机制：空闲 300 秒会暂停，所以编译期间脚本把 idleTimeout 临时调到 1800 秒，用 `trap` 保证退出时恢复，并轮询保活。
- 限制：单次 exec 上限 5 分钟，所以编译后台化；VM 30 天自动删除；磁盘 32G，要定期 `cargo clean`。
- 产物回传：用 `freestyle vm fs read`，实测 5.5MB 用 3 秒（约 13Mbps）。经 chfs 中转的链路目前不可用（上传返回 201 但文件未写入）。

### 4.5 Simular Box（云桌面）

- box_id `cb-7da86dbe1c84`，名称 `main`，镜像 `sbxdesk:v13`。
- 2 核 Xeon Platinum 8581C，3.79GiB 内存，20GB 磁盘；Ubuntu + XFCE 桌面（1280x800）。
- 出口 IP 在美国爱荷华（GCP us-central1），是数据中心 IP，可用来验证境外访问。
- 通过 MCP 暴露 37 个工具：盒子生命周期、文件读写（`/workspace`）、持久 bash 会话、浏览器自动化、桌面截图与可访问性树、人工接管（box_view）等。
- 已装 gcc、make、python3、node、git、Google Chrome、LibreOffice；**没有 cargo、go、Docker**。
- 单核比本机快 25–38%（gcc 编译基准 461ms 对本机 618ms），但只有 2 核 3.8G，Rust 全量编译没有优势。
- 账号状态：`box plans are not enabled`，处于试用，额度和稳定性不明。
- 定位：**眼睛和手**。适合抓取需要 JS 渲染的网页、填写网页后台表单、截图验证 UI、2FA 或支付等需要人工介入的流程、操作桌面软件、从美国 IP 验证访问。
- 可探索：它有持久 bash，理论上可以按最初思路在里面运行 vc agent 变成 Hub 节点，用来做"带界面的验证节点"。受限于配置和休眠，只做验证，不做编译。此项未实测。

### 4.6 hwsg 及其他历史节点

- `hwsg`、`hwhk`、`wjyhk` 出现在 Hub 审计日志中，但当前设备表和凭据表里都没有，只有 `wjy5010u`、`muse-01`、`cue-01`。
- `wjyhk`：旧开发机和旧 Hub 主机，10 月中旬起不可达，见 `docs/recovery/` 下恢复文档。
- `hwsg`：Muse 规范里把它和本机并列，称为"不会被平台替换的机器"，推荐用来跑半小时以上的大构建。审计里它最后一次出现是 11 月 1 日，当时 exec 被安全策略拒绝。
- `hwhk`：仅 10 月 15 日一条被拒记录，用途不明。
- 待办：确认 hwsg 的配置和当前是否可重新接入。若可用，它比任何平台沙箱都适合做长任务的稳定节点。

## 五、选型决策矩阵

| 任务 | 首选 | 备选 | 理由 |
|---|---|---|---|
| deva-rust 全量 release / `cargo test` | cue-01 | Freestyle，再次是 hwsg（若可用） | cue-01 配置最强且无额度限制；Freestyle 实测最稳 |
| 增量编译、`cargo check` | 本机 | muse-01（Go） | 传输和启动开销超过收益 |
| Go 项目增量构建 | 本机 | muse-01 | 缓存命中后较快，muse-01 注意会被重建 |
| 需要 Docker | Freestyle | — | 只有它装了 Docker |
| 需要干净 Ubuntu 24.04 环境 | Freestyle | — | 本机是 Debian |
| 超过 30 分钟的冷编译 | hwsg 或本机 | cue-01（验证生命周期后） | muse-01 和 Freestyle 的会话会被打断 |
| 网页抓取（JS 渲染）、表单操作、UI 截图 | Simular Box | — | 有真实 Chrome 和桌面 |
| 需要人工接管（2FA、支付） | Simular Box | — | 支持 box_view 接管 |
| 验证境外或美国 IP 访问 | Simular Box | Freestyle | Simular 出口在美国 |
| 常驻服务、源码、持久数据 | 本机 | — | 只有本机是常驻且持久的 |

## 六、统一编译流程（抽象成"后端接口"）

`cloud-build.sh` 的 10 步本质上是一个后端接口。建议把脚本改成按后端名分派：

```
cloud-build.sh <后端> [--test | --check | --sync-only]
后端：local | freestyle | hub:<节点名>
```

每个后端实现同样的五个动作：

| 动作 | local | freestyle | hub 节点（cue-01 / muse-01） |
|---|---|---|---|
| 准备环境 | 无 | 唤醒 VM，调大 idleTimeout，缺 Rust 则安装 | 检查 cargo；muse-01 先 source env.sh 并设代理 |
| 同步源码 | 无 | tar 打包后 `fs write` 上传 | 节点上 `git clone/pull`；无可用远端时用 tar 分块写入 |
| 启动编译 | 直接 cargo | 后台 nohup | 后台 `nohup`/`setsid nohup`，日志和完成标记写入持久目录 |
| 轮询 | — | 每 30 秒一次，兼作保活 | 用短 `exec_command` 读日志和标记，禁止 sleep 长轮询 |
| 取回产物 | 无 | `fs read`（约 3 秒） | 见第七节，待选定 |
| 清理 | 无 | 恢复 idleTimeout=300 | 清理工作目录或保留缓存 |

产物统一输出为 `target/release/deva-cloud`，避免覆盖本地产物。

## 七、产物回传方案（待选定）

| 方案 | 做法 | 状态 |
|---|---|---|
| A. Hub `read_file` 分页 | 每次最多读 1MB，5.5MB 需约 6 次，再拼接校验 sha256 | 需先确认二进制读取是否安全，可能要先 base64 |
| B. 节点推送到 chfs | cue-01 能出外网，可直接上传 | VM→chfs 经 Cloudflare 一直失败（返回 201 但不写入），需要先排查 |
| C. 节点推送到对象存储或 Git release | 通用，不依赖 Hub | 需要新增一个存储位置和凭据，凭据不能放进沙箱长期保存 |

建议先验证 A，确认可行就不引入新依赖。

## 八、约束与规则（必须遵守）

来自 `/opt/dev/AGENT-RULES.md` 和 Hub 限速参数：

- 每个令牌同时最多 2 个在途 exec，建议只用 1 个；请求速率不超过 10 次/秒。
- 每个节点最多 4 个远端会话。
- 同步 exec 上限 300 秒。`exec_start` 在远端最长 30 分钟，但会占用并发槽，长任务改用 `nohup` 启动后立即返回，再用短命令轮询。
- 被限流后立即停止，等待 30 秒后只发一条 `echo ok` 探测，不要连续重试或重复提交重负载命令。
- 全量构建一次只跑一轮，结束后先做轻量只读检查再决定下一步。
- muse-01 的红线：不要动 agent 和中继服务、不要改 `/home/hatch/workspace/vps-agent/`、不要重跑 install.sh、密钥令牌不得经 Hub 命令传递。
- 产物和日志不要依赖节点本地的非持久目录；muse-01 只用家目录，cue-01 的持久性验证前同样对待。

## 九、安全问题

1. `deva/docs/cloud-build-usage.md` 与 `cloud-build-test-report-20261110.md` 中有**明文凭据**：chfs 的账号密码（报告里完整出现）、Freestyle API Key 的前缀；`cloud-build.sh` 脚本里还内置了完整的 Key。这些文件在共享仓库里，违反共享环境规范第五节第 3 条。
   - 处理：更换 chfs 密码；Freestyle Key 改为环境变量或 `/etc` 下的 env 文件读取；文档中只保留占位符。
2. Hub 具备任意 Shell 执行能力。远端节点应使用独立、可吊销的节点令牌，沙箱里不保存长期密钥。
3. 临时节点上的任何凭据都视为随时丢失，只给只读 deploy key 或一次性令牌。

## 十、待办与未决

| # | 事项 | 优先级 |
|---|---|---|
| 1 | 在 cue-01 上做一次最小验证：clone deva-rust，跑 `cargo build --release`，记录耗时和依赖拉取情况 | 高 |
| 2 | 选定产物回传方案（第七节），并实测一次 sha256 校验 | 高 |
| 3 | 轮换已出现在文档和脚本里的凭据（第九节） | 高 |
| 4 | 观察 cue-01 的存活周期，确认是否会被回收 | 中 |
| 5 | 确认 hwsg 的配置与当前状态，决定是否重新接入 Hub | 中 |
| 6 | 把 `cloud-build.sh` 改成按后端分派，增加 `hub:<节点>` | 中 |
| 7 | 评估在 Simular Box 内运行 vc agent 作为验证节点（GUI 与美国 IP），不做编译 | 低 |
| 8 | 查清 VM→chfs 上传返回 201 但不写入的根因 | 低 |

## 十一、资料来源

- `deva/docs/`：`cloud-build-usage.md`、`cloud-build-feasibility.md`、`cloud-build-test-report-20261110.md`、`cloud-sandbox-comparison.md`、`task-distribution-across-machines.md`
- `vps-commander/docs/muse-vm-host-usage-guide.md`
- `/opt/dev/AGENT-RULES.md`、`/opt/dev/README-SHARED-ENV.md`
- Hub 设备表与审计日志（`commander.db`，只读查询）和 2026-11-12 对 cue-01、muse-01 的实测
- 未实测项（Simular Box、Freestyle 的当前状态）沿用 2026-11-09 至 11-11 的文档数据，使用前请重新核对
