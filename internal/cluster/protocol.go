package cluster

import "github.com/wjyhk/vps-commander/internal/sysinfo"

type Message struct {
	ID         string           `json:"id,omitempty"`
	Event      string           `json:"event,omitempty"`
	Action     string           `json:"action,omitempty"`
	Device     string           `json:"device,omitempty"`
	Status     string           `json:"status,omitempty"`
	Payload    any              `json:"payload,omitempty"`
	Profile    *sysinfo.Profile `json:"profile,omitempty"`
	ExitCode   int              `json:"exit_code,omitempty"`
	Stdout     string           `json:"stdout,omitempty"`
	Stderr     string           `json:"stderr,omitempty"`
	DurationMS int64            `json:"duration_ms,omitempty"`
	Error      string           `json:"error,omitempty"`
	TS         int64            `json:"ts,omitempty"`
}

type ExecPayload struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type FileReadPayload struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
}

type FileWritePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
