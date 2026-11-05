package mcp

// ToolAnnotations are the MCP tool hints a client uses to decide whether a call needs confirmation.
// All four hints are always set explicitly; the values follow the tool's real behaviour.
type ToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

func boolPtr(v bool) *bool { return &v }

// readOnlyTools only read state. They never change a node or the Hub.
var readOnlyTools = map[string]bool{
	"list_devices": true, "read_file": true, "read_multiple_files": true, "list_processes": true,
	"diagnostic_command": true, "list_agent_mcp": true, "exec_status": true, "memo_get": true,
}

// destructiveTools change state on a node (or cancel work running there).
var destructiveTools = map[string]bool{
	"exec_command": true, "exec_start": true, "exec_cancel": true, "write_file": true, "edit_block": true,
	"create_directory": true, "move_file": true, "call_agent_mcp": true,
}

// localTools only touch the Hub's own session state and never reach a node.
var localTools = map[string]bool{"memo_set": true, "memo_get": true}

// annotationsFor returns the hints for a tool. Unknown tools get the most conservative values
// (not read-only, destructive, open world), so a newly added tool that is not classified is never
// presented to clients as safe.
func annotationsFor(name string) ToolAnnotations {
	a := ToolAnnotations{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(!localTools[name]),
	}
	switch {
	case readOnlyTools[name]:
		a.ReadOnlyHint = boolPtr(true)
		a.DestructiveHint = boolPtr(false)
		a.IdempotentHint = boolPtr(true)
	case name == "memo_set":
		a.DestructiveHint = boolPtr(false)
	case destructiveTools[name]:
		// keep the conservative defaults above
	}
	return a
}
