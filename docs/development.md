# 开发、测试与发布

适用版本：v1.1.30。

## 1. 工程原则

1. 认证与授权分离；scope、设备白名单、节点安全等级各自独立检查。
2. 工具风险在 Tool Registry 中静态注册，节点策略运行时解析，默认 fail-closed。
3. 所有调用（含拒绝）写审计，审计内容先脱敏再截断。
4. Session 必须有 TTL 与资源上限。
5. 生产二进制可追溯（干净工作树构建），发布可回滚。
6. 凭据只走环境变量 / 配置文件，不进入 argv、URL、日志或仓库。

## 2. 构建

```bash
make test          # go test ./...
make race          # go test -race ./...
make vet
make linux-amd64   # 输出到 bin/
make linux-arm64
make mcp-stdio
```

发布构建参数：`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"`。

## 3. 提交前检查

```bash
gofmt -l ./cmd ./internal      # 应无输出
go vet ./...
go test -count=1 ./...
git diff --check
node --check internal/web/static/app.js
```

改动面板时还需运行真实 Hub 端到端测试（需要 Chromium 与 Python `websocket-client`）：

```bash
python3 internal/web/testdata/audit-layout.test.py
```

它会构建临时 Hub（可用 `VPC_TEST_PRODUCTION_DB` 指向一份数据库副本，脚本只以只读方式复制，不会写入原库），用 headless Chromium 在 1280 / 390 / 320 宽度下验证分区、令牌管理、AI 接入向导、一次性密钥框、剪贴板、审计表与 Streamable HTTP / SSE 连通。

## 4. 验收门禁

发布前至少确认：

| 类别 | 验收项 |
| :--- | :--- |
| 构建 | 干净工作树；amd64 / arm64 / armv7 均可编译；单元测试与面板 E2E 通过 |
| 认证 | 无效令牌 401；URL 中的令牌不被接受；吊销后立即失效；`LEGACY_KEY=off` 时 legacy 密钥失效 |
| 授权 | 每个 REST 接口与 MCP 工具在缺 scope 时 403 并写 `denied` 审计；设备白名单生效；`tools/list` 按 scope 过滤 |
| 安全等级 | Medium 节点拒绝 `exec_command`、允许文件修改；未注册 / 离线节点拒绝；策略版本不回退 |
| 审计 | 操作人、传输方式、目标、结果正确；命令脱敏截断；`write_file` 不记录内容 |
| MCP | Streamable HTTP 与 SSE 的 initialize / tools/list / tools/call；会话归属校验；`call_agent_mcp` 无旁路 |
| 集群 | Hub 重启后 Agent 自动恢复；在线数与升级前一致 |
| 资源 | Session 上限与 TTL 生效；限流返回 429 + `Retry-After` |
| 文档 | 新增环境变量、接口、工具同步到 docs；不含真实令牌、密码、域名或内网地址 |

出现以下任一情况立即停止发布并回滚：Agent 断联、未授权请求成功、未知设备获得 High、MCP 旁路、凭据出现在 argv、审计丢失、策略版本回退、资源无限增长。

规模化验收（500 Agent / 50 MCP）见 [loadtest.md](loadtest.md)。

## 5. 发布

1. 功能分支经审核通过后快进合入 `main` 并推送；
2. 推送下一个 `vX.Y.Z` tag，GitHub Actions（`.github/workflows/release.yml`）构建 Hub / Agent 的 amd64、arm64、armv7 以及 mcp-stdio amd64，生成 `checksums.txt`，并附带 `install.sh`、`upgrade-hub.sh`、`upgrade-agent.sh`；
3. 确认 Release 产物齐全后，按 [operations.md](operations.md#2-升级-hub) 升级生产 Hub，再逐台升级 Agent；
4. 在 [CHANGELOG.md](../CHANGELOG.md) 记录变更。

## 6. 文档同步规则

新增或修改功能时同步检查：`README.md`、`docs/architecture.md`、`docs/security.md`、`docs/mcp-clients.md`、`docs/api.md`、`docs/deploy/*`、`docs/operations.md`、`openapi.json`（影响 HTTP 契约时）。示例一律使用 `hub.example.com`、`node-01` 和 `<TOKEN>` 之类占位符。
