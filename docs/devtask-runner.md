# devtask-runner

独立进程，使用 `devtask:run` 令牌从 Hub 领取任务并在隔离目录中执行。

## 环境变量

| 变量 | 说明 |
|------|------|
| `HUB_URL` | Hub 根地址，如 `http://192.168.20.132:9521` |
| `RUNNER_TOKEN` | 仅含 `devtask:run` 的命名令牌 |
| `RUNNER_ID` | 可选，claim 的 claimed_by |
| `WORK_ROOT` | 任务工作目录根，默认 `/var/lib/devtask-runner` |
| `LEASE_SEC` | 租约秒数，默认 120 |
| `POLL_SEC` | 无任务时轮询间隔，默认 5 |
| `MODEL_API_BASE` | OpenAI 兼容 API 根路径 |
| `MODEL_API_KEY` | 模型密钥（仅环境注入，勿入库） |

## 运行

```sh
export HUB_URL=... RUNNER_TOKEN=... MODEL_API_BASE=... MODEL_API_KEY=...
./devtask-runner          # 常驻
./devtask-runner -once    # 最多处理一个任务后退出
```

## 安全要点

- 路径牢笼：所有文件操作限制在任务 worktree 内，拒绝 `.git` 写入与 symlink 逃逸。
- 命令白名单：验收命令按任务/项目前缀匹配，不经 shell。
- 模型错误（429/401/5xx）→ `blocked_model`，不切换模型。
- Hub 侧 `DEVTASK_ENABLED` 关闭时 claim 不可用。
