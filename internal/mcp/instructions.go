package mcp

import (
	"strings"

	"github.com/wjyhk/vps-commander/internal/auth"
)

// serverInstructions returns the server-level protocol notes sent in the initialize result.
// Sections are included only for the scopes the token holds, so instructions never point at a tool
// the caller cannot use. The text stays below maxInstructionsLen.
const maxInstructionsLen = 1500

func serverInstructions(p auth.Principal, authed bool) string {
	has := func(scope string) bool { return authed && p.HasScope(scope) }
	var b strings.Builder
	b.WriteString("vps-commander 是受设备权限控制的远程执行网关。\n")
	b.WriteString("【设备】设备名只能来自 list_devices 返回的名称；不传 device 时在 Hub 所在节点执行。\n")
	if has("exec") {
		b.WriteString("【执行】同步 exec_command 有时长上限；预计超过 30 秒的命令用 exec_start 启动，再用 exec_status 轮询；取消用 exec_cancel。\n")
		b.WriteString("【输出】输出被截断时，用 read_file 且 device 填 @hub-spill、path 填 spill_path 分段读取完整输出；每次都检查 exit_code，非零退出码不会标记为错误。\n")
	}
	if has("files:write") {
		b.WriteString("【写入】修改已有文件优先用 edit_block；write_file 会整体覆写，只在确认目标时使用。\n")
	}
	if has("files:read") || has("files:write") {
		b.WriteString("【文件】读取大文件用 offset 与 limit 分页。\n")
	}
	if has("process") {
		b.WriteString("【进程】list_processes 只读；结束进程前先确认目标。\n")
	}
	if has("mcp:call") {
		b.WriteString("【MCP 服务】call_agent_mcp 调用节点上的 MCP 服务，先用 list_agent_mcp 确认服务与工具名。\n")
	}
	b.WriteString("【安全】破坏性操作（删除、覆盖、停止服务）执行前先确认目标；不得把令牌、密钥或 webhook 写入命令、便签或日志。\n")
	b.WriteString("【便签】memo_set / memo_get 只保存当前会话的结论，不保存秘密。\n")
	r := []rune(b.String())
	if len(r) > maxInstructionsLen {
		r = r[:maxInstructionsLen]
	}
	return string(r)
}
