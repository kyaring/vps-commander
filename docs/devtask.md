# DevTask

> **状态（2026-10-28）**：完整说明与 runner 文档在 feature 分支上维护，**尚未合入 main**。

| 内容 | 分支（示例） |
|------|----------------|
| 总览、开关、scope、部署要点 | `feat/devtask-d6-e2e-docs`（`docs/devtask.md`） |
| runner 环境变量与运行 | `feat/devtask-d5-runner`（`docs/devtask-runner.md`） |
| 面板与通知事件 | `feat/devtask-b-panel-notify` |

进度与待审卡见 `docs/roadmap.md`、`docs/tasks/BOARD.md`（T-20261028-04～09）。启用开关为 `DEVTASK_ENABLED`（默认关闭）。

规格裁定（2026-11-11）：admin 不隐含 devtask:run；模型白名单为空 = 拒绝一切模型；模型 URL/key 方案 B1 的安全边界已复核接受，见 docs/devtask-spec-amendment-1028.md。
