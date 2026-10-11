# Agent exec 使用规范

适用于通过 VPS-Commander `exec_command`（MCP）或 `POST /api/v1/exec`（REST）在节点上执行命令的开发 / 运维 agent。

背景：2026-10-11 对 gt5-chatgpt 的 300 次 exec 做了排查，34 次在审计里显示 error（11%），其中没有一次是 Hub 故障；另有 6 次命令实际失败，却显示 ok。

## 审计结果含义（v1.1.34 起）

| result | 含义 |
|---|---|
| `ok` | 命令退出码为 0 |
| `exit_nonzero` | 命令已在节点上执行完，退出码非 0。是命令自身的结果，与 Hub 无关 |
| `error` | Hub、agent 或工具调用本身失败，比如文件不存在或参数无效 |
| `denied` | 被 scope、设备白名单或安全策略拒绝 |

命令非零退出时，如果 stderr 为空，`error_msg` 会保存 stdout 末尾最多 1024 个字符，并加上 `stdout:` 前缀。stderr 本身也只保留末尾 1024 个字符。

## 规范

1. **纯查询不要制造失败。** grep 没有匹配会返回 1。查询链最后一条是 grep 时，结尾加 `|| true`，或者改用 `grep -c`。
2. **补丁脚本必须失败即退出。** 找不到锚点时要 `sys.exit(1)` 或 `raise SystemExit("...")`，绝不能只 `print("marker not found")`，否则补丁没打上，结果却显示 ok。替换前先检查匹配次数（`s.count(old) == 1`）。
3. **改完 Go 立即验证。** 每次补丁之后马上跑 `gofmt -l` 和 `go build ./...`，不要攒到最后跑 go test 时才发现编译错误。
4. **整文件写入优于片段替换。** 改 Go 代码时，优先用 `cat > file <<'EOF'` 写完整文件或完整函数。用 python 的 `str.replace` 处理带 `\t`、`\n` 的片段很容易把字面反斜杠写进源码。
5. **先建目录再写文件**：`mkdir -p dir && cat > dir/file`。
6. **长文本走文件或 base64。** 含引号、中文或很长的 prompt，先写进文件再读取，或者 base64 后再解码，不要直接内联在 heredoc 的 python 字符串里。
7. **构建 / 测试链用 `set -e`；查询链不用。** 查询链加了 `set -e`，一条没匹配就会中断整串。
8. **不要假定工具存在。** wjyhk 上没有 `rg`、`/usr/bin/time`、`bc`。用 grep、shell 内置的 time、python 代替。路径不确定时先 `ls` 确认。
9. **一次 exec 只做一件可判定的事。** 把查询和修改分开，这样退出码才能说明问题。
10. **看退出码，不只看输出。** MCP 对非零退出不设 `isError`，调用方要自己检查返回 JSON 里的 `exit_code`。
