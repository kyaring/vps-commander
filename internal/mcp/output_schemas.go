package mcp

// outputSchemas describes successful structuredContent returned by data tools.
// The memo tools are deprecated; flat write acknowledgements are intentionally omitted.
func outputSchemas() map[string]map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	object := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	requiredObject := func(required ...string) map[string]any {
		return map[string]any{"type": "object", "required": required}
	}
	array := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }

	execFields := map[string]any{
		"device": str(), "exit_code": integer(), "stdout": str(), "stderr": str(), "duration_ms": integer(),
		"truncated": boolean(), "total_bytes": integer(), "total_lines": integer(), "sha256": str(),
		"spill_path": str(), "spill_device": str(), "timed_out": boolean(),
	}
	execRequired := []string{"device", "exit_code", "stdout", "stderr", "duration_ms", "truncated", "total_bytes", "total_lines", "sha256", "spill_path", "spill_device", "timed_out"}
	jobRequired := append(append([]string{}, execRequired...), "job_id", "status", "reason", "started_at", "max_runtime_s")
	devTaskStatus := requiredObject("task_id", "project", "title", "status", "round", "max_rounds", "model", "branch", "base_sha", "commit_sha", "test_summary", "result_md", "error_code", "error_detail")

	return map[string]map[string]any{
		"exec_command": object(execFields),
		"read_file": object(map[string]any{
			"device": str(), "path": str(), "offset": integer(), "content": str(), "bytes": integer(),
			"total_size": integer(), "has_more": boolean(), "eof": boolean(),
		}),
		"read_multiple_files": array(object(map[string]any{"path": str(), "content": str(), "error": str(), "truncated": boolean()})),
		"list_devices":        array(object(map[string]any{"name": str(), "status": str(), "is_local": boolean()})),
		"list_processes":      array(object(map[string]any{"pid": integer(), "command": str(), "cpu": str(), "memory_kb": integer()})),
		"diagnostic_command": object(map[string]any{
			"action": str(), "target": str(), "exit_code": integer(), "stdout": str(), "stderr": str(), "duration_ms": integer(),
		}),
		"exec_start":  requiredObject(jobRequired...),
		"exec_status": requiredObject(jobRequired...),
		"exec_cancel": requiredObject(jobRequired...),
		"list_agent_mcp": map[string]any{"anyOf": []any{
			map[string]any{"type": "object", "required": []string{"device", "services"}},
			array(requiredObject("id", "name", "transport")),
		}},
		"call_agent_mcp": object(map[string]any{
			"server": str(), "tool": str(), "result": map[string]any{}, "error": str(), "duration_ms": integer(),
		}),
		"dev_task_dispatch": object(map[string]any{"task_id": str(), "branch": str(), "model": str()}),
		"dev_task_status":   devTaskStatus,
		"dev_task_list": object(map[string]any{"tasks": array(object(map[string]any{
			"task_id": str(), "project": str(), "title": str(), "status": str(), "round": integer(), "model": str(), "branch": str(), "created_at": integer(),
		}))}),
		"dev_task_diff": object(map[string]any{
			"task_id": str(), "base_sha": str(), "commit_sha": str(), "offset": integer(), "diff": str(), "total_bytes": integer(), "note": str(),
		}),
		"dev_task_logs": object(map[string]any{"events": array(object(map[string]any{
			"task_id": str(), "seq": integer(), "ts": integer(), "round": integer(), "type": str(), "payload": map[string]any{},
		}))}),
		"dev_task_revise": devTaskStatus,
		"dev_task_cancel": object(map[string]any{"ok": boolean(), "task_id": str(), "status": str()}),
	}
}

var toolOutputSchemas = outputSchemas()

func applyOutputSchemas(tools []Tool) {
	for i := range tools {
		if schema, ok := toolOutputSchemas[tools[i].Name]; ok {
			tools[i].OutputSchema = schema
		}
	}
}

func hasStructuredOutput(tool string) bool {
	_, ok := toolOutputSchemas[tool]
	return ok
}
