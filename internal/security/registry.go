package security

type ToolSpec struct {
	Name         string
	RequiredRisk int
	Category     string
	ReadOnly     bool
}

var registry = map[string]ToolSpec{
	"read_file":               {Name: "read_file", RequiredRisk: RiskLow, Category: "file", ReadOnly: true},
	"read_multiple_files":     {Name: "read_multiple_files", RequiredRisk: RiskLow, Category: "file", ReadOnly: true},
	"list_directory":          {Name: "list_directory", RequiredRisk: RiskLow, Category: "file", ReadOnly: true},
	"get_file_info":           {Name: "get_file_info", RequiredRisk: RiskLow, Category: "file", ReadOnly: true},
	"start_search":            {Name: "start_search", RequiredRisk: RiskLow, Category: "search", ReadOnly: true},
	"get_more_search_results": {Name: "get_more_search_results", RequiredRisk: RiskLow, Category: "search", ReadOnly: true},
	"list_processes":          {Name: "list_processes", RequiredRisk: RiskLow, Category: "process", ReadOnly: true},
	"diagnostic_command":      {Name: "diagnostic_command", RequiredRisk: RiskLow, Category: "diagnostic", ReadOnly: true},
	"list_sessions":           {Name: "list_sessions", RequiredRisk: RiskLow, Category: "process", ReadOnly: true},
	"read_process_output":     {Name: "read_process_output", RequiredRisk: RiskLow, Category: "process", ReadOnly: true},
	"edit_block":              {Name: "edit_block", RequiredRisk: RiskMedium, Category: "file", ReadOnly: false},
	"write_file":              {Name: "write_file", RequiredRisk: RiskMedium, Category: "file", ReadOnly: false},
	"create_directory":        {Name: "create_directory", RequiredRisk: RiskMedium, Category: "file", ReadOnly: false},
	"move_file":               {Name: "move_file", RequiredRisk: RiskMedium, Category: "file", ReadOnly: false},
	"exec_command":            {Name: "exec_command", RequiredRisk: RiskHigh, Category: "process", ReadOnly: false},
	"start_process":           {Name: "start_process", RequiredRisk: RiskHigh, Category: "process", ReadOnly: false},
	"interact_with_process":   {Name: "interact_with_process", RequiredRisk: RiskHigh, Category: "process", ReadOnly: false},
	"kill_process":            {Name: "kill_process", RequiredRisk: RiskHigh, Category: "process", ReadOnly: false},
	"force_terminate":         {Name: "force_terminate", RequiredRisk: RiskHigh, Category: "process", ReadOnly: false},
	"mcp_call":                {Name: "mcp_call", RequiredRisk: RiskMedium, Category: "mcp", ReadOnly: false},
}

var actionTools = map[string]string{
	"read": "read_file", "probe": "read_file", "write": "write_file",
	"mcp": "mcp_call", "exec": "exec_command", "diagnostic": "diagnostic_command",
}

func Lookup(name string) (ToolSpec, bool) {
	v, ok := registry[name]
	return v, ok
}

func RequiredRiskForAction(action string) (int, bool) {
	name, ok := actionTools[action]
	if !ok {
		return 0, false
	}
	spec, ok := registry[name]
	if !ok {
		return 0, false
	}
	return spec.RequiredRisk, true
}
