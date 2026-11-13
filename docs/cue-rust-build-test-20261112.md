# cue-01 Rust Release 构建与产物回传实测记录

- 测试日期：2026-11-12
- 执行节点：cue-01
- 控制/接收节点：wjy5010u
- 项目：deva-rust
- 测试类型：单次冷启动 `cargo build --release`、构建产物回传、完整性校验及接收端基本可执行性检查
- 总体结论：**构建成功，三个可执行文件均已回传，SHA256 与源节点逐一一致**

## 1. 测试目标

验证 cue-01 是否可以作为 VPS-Commander 的 Rust 编译执行节点，并验证通过 Hub 将编译产物安全、完整地取回控制节点。

本次遵守全量构建只执行一轮的约束，没有在构建结束后重复执行全量编译。

## 2. 测试环境与代码版本

| 项目 | 实测值 |
|---|---|
| 编译节点 | cue-01 |
| 操作系统架构 | Linux x86_64 |
| Rust | 1.97.1 |
| Cargo | 1.97.1 |
| 内存 | 约 23 GiB |
| 构建期间可用内存 | 约 21 GiB |
| 构建期间磁盘可用空间 | 约 37 GiB |
| 仓库 | `https://gitea.king.nyc.mn/Kory/deva-rust.git` |
| 分支 | master |
| Git commit | `2e886810731927c0e0c567d436755088e6f4e909` |

代码检出目录：

`/root/cue-deva-rust-build-20261112/repo`

构建 commit 与 wjy5010u 本地对应项目版本一致。

## 3. cue-01 构建流程

1. 检查 Rust/Cargo 版本、CPU/内存和磁盘资源。
2. Clone `deva-rust` 仓库，并核对 commit。
3. 在仓库目录执行一次 `cargo build --release`。
4. 使用后台 `nohup` 执行，避免长时间同步占用 Hub 执行会话。
5. 通过日志和状态文件轮询构建状态。
6. 构建结束后检查退出码、产物、ELF 类型和 Git 工作区。

现场目录：

```text
/root/cue-deva-rust-build-20261112/
├── repo/
│   └── target/release/
├── build.log
├── clone.log
├── commit.txt
├── pid
├── start.txt
└── status.txt
```

构建日志：`/root/cue-deva-rust-build-20261112/build.log`

状态文件：`/root/cue-deva-rust-build-20261112/status.txt`

状态文件记录：

```text
exit_code=0
finished_at=2026-11-11T23:04:44Z
```

构建耗时：**6 分钟**。

构建期间先出现 crates.io 索引更新和依赖下载，随后进入依赖及项目编译。当前日志未单独记录各阶段耗时，因此不能把 6 分钟全部归因于编译或下载。

LTO 阶段观察到 rustc CPU 使用率峰值约 599%；构建期间没有发现内存或磁盘空间不足。

构建输出有两个函数指针转换 warning（`crates/deva-cli/src/main.rs` 第 424、425 行），没有阻止构建成功。

## 4. 构建产物清单

实际产物名称与架构文档中预期的 `deva-cloud` 不同，项目本次实际生成以下三个可执行文件：

| 文件 | 大小（bytes） | SHA256 |
|---|---:|---|
| `deva` | 5,644,608 | `3f26ad50945f75bff1d512d828ac605a53e72122f694b75f0d51bd75f56d6502` |
| `deva-exec-rpc` | 2,660,248 | `b47345301bfddd1353851c12b6c5c1be7cd2b978606aed6609888acf7eda3751` |
| `deva-sec-rpc` | 2,030,048 | `91fba23b156cb9f0b1b520aeb86b02dd73ebee908c5f6247b6803b0513f4899e` |

三个文件均为 Linux x86-64 ELF 64-bit PIE executable，且为 stripped 文件。

## 5. 通过 Hub 回传产物

### 5.1 回传方式

本次实测采用 Hub 文件读取能力传输，没有使用外部对象存储、chfs 或额外长期凭据。

由于产物是二进制文件，传输前在 cue-01 将文件 Base64 编码为文本，再分块：

- 每个文本分块目标大小：900,000 bytes。
- 通过 Hub 的多文件读取能力逐块读取。
- 在 wjy5010u 端按文件名和分块序号写入临时文件。
- 按序拼接 Base64 分块，再解码为原始二进制文件。
- 对接收端文件重新计算 SHA256，与 cue-01 源文件哈希逐一比较。

本次三个文件共传输 17 个 Base64 分块。传输完成后，源节点的临时 Base64 文件及分块已清理；原始构建产物、仓库和日志均保留。

### 5.2 接收端保存位置

wjy5010u：

`/home/orca/projects/vps-commander/artifacts/cue-deva-rust-20261112/`

目录内容：

```text
artifacts/cue-deva-rust-20261112/
├── deva
├── deva-exec-rpc
└── deva-sec-rpc
```

### 5.3 完整性校验

接收端重新计算的 SHA256：

| 文件 | 接收端 SHA256 | 与 cue-01 一致 |
|---|---|---|
| `deva` | `3f26ad50945f75bff1d512d828ac605a53e72122f694b75f0d51bd75f56d6502` | 是 |
| `deva-exec-rpc` | `b47345301bfddd1353851c12b6c5c1be7cd2b978606aed6609888acf7eda3751` | 是 |
| `deva-sec-rpc` | `91fba23b156cb9f0b1b520aeb86b02dd73ebee908c5f6247b6803b0513f4899e` | 是 |

**三个文件大小和 SHA256 均与 cue-01 源文件一致，回传完整性校验通过。**

### 5.4 接收端可执行性检查

回传后在 wjy5010u 执行：

- 将三个文件权限设为 `755`。
- `file` 确认均为 x86-64 ELF 可执行文件。
- 对主程序执行 `ldd`，动态依赖可解析，依赖包括系统的 `libgcc_s` 和 `libc`。
- 执行 `./deva --help`，退出码为 0，能够正常输出 CLI 帮助。

注意：本次没有启动两个 RPC 服务进程，也没有执行真实 API 请求或端到端业务流程。因此这里只能认定文件回传、校验及主程序基础启动检查通过，不能视为完整运行验收。

## 6. 最终结果

| 检查项 | 结果 | 说明 |
|---|---|---|
| cue-01 Rust 环境 | PASS | Rust/Cargo 版本正常 |
| 仓库版本一致性 | PASS | commit 与控制节点一致 |
| Release 构建 | PASS | 单次构建，耗时 6 分钟，退出码 0 |
| 产物生成 | PASS | 得到 3 个 ELF 可执行文件 |
| Hub 产物回传 | PASS | 3 个文件均已取回 |
| SHA256 完整性 | PASS | 三个文件哈希逐一匹配 |
| 接收端主程序基础检查 | PASS | `deva --help` 退出码 0 |
| RPC 服务运行验证 | NOT TESTED | 未启动服务进程 |
| API/端到端验证 | NOT TESTED | 未执行真实业务请求 |
| 多任务并发编译 | NOT TESTED | 本轮只进行单任务验证 |

## 7. 遗留问题与建议

1. **产物命名约定：** 架构文档预期 `target/release/deva-cloud`，本次实际主程序为 `deva`。应核对文档或项目构建配置，统一约定。
2. **冷构建耗时拆分：** 下一轮性能评估应区分依赖下载、依赖编译、项目编译和 LTO 链接耗时；不要为了测量而重复本轮全量构建。
3. **缓存能力：** 可评估 Cargo registry/cache 持久化和 sccache，提升重复任务效率。
4. **运行验证：** 后续单独设计 RPC 子程序启动、健康检查和 API 集成测试，避免直接使用生产密钥。
5. **安全整改：** 架构文档已记录共享仓库存在明文凭据风险，应独立安排凭据轮换及敏感内容清理。
6. **节点生命周期：** 继续观察 cue-01 的存活周期及构建目录持久性。
7. **调度接入：** 本次证明了手动通过 Hub 在 cue-01 执行构建和回传产物的可行性，但不代表 DevTask 调度器已经完成节点注册或自动分派能力验证。

## 8. 结论

本次完成了从 cue-01 拉取代码、执行 Rust Release 构建、生成产物，到通过 Hub 回传 wjy5010u、逐个校验 SHA256、检查主程序基础启动的完整闭环。

**结论：cue-01 的单任务 Rust Release 构建与 Hub 产物回传链路均已实测通过。**

本次测试未修改项目源码，未重复全量构建，未操作 muse-01，也未清理原始构建现场。接收端产物目录与本文档均保留在 VPS-Commander 工作目录下。

---
