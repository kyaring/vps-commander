package cluster

import "github.com/wjyhk/vps-commander/internal/sysinfo"

type Message struct {
	ID              string           `json:"id,omitempty"`
	Event           string           `json:"event,omitempty"`
	Action          string           `json:"action,omitempty"`
	SubAction       string           `json:"sub_action,omitempty"`
	Device          string           `json:"device,omitempty"`
	Arch            string           `json:"arch,omitempty"`
	OS              string           `json:"os,omitempty"`
	Status          string           `json:"status,omitempty"`
	Payload         any              `json:"payload,omitempty"`
	Profile         *sysinfo.Profile `json:"profile,omitempty"`
	ExitCode        int              `json:"exit_code,omitempty"`
	Stdout          string           `json:"stdout,omitempty"`
	Stderr          string           `json:"stderr,omitempty"`
	DurationMS      int64            `json:"duration_ms,omitempty"`
	Error           string           `json:"error,omitempty"`
	TS              int64            `json:"ts,omitempty"`
	ProtocolVersion string           `json:"protocol_version,omitempty"`
	AgentVersion    string           `json:"agent_version,omitempty"`
	Capabilities    []string         `json:"capabilities,omitempty"`
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

type EditBlockPayload struct {
	Path         string `json:"path"`
	OldText      string `json:"old_text"`
	NewText      string `json:"new_text"`
	ExpectedHash string `json:"expected_hash,omitempty"`
}

type EditBlockResult struct {
	BytesWritten int64 `json:"bytes_written"`
}

type StartProcessPayload struct {
	Command      string `json:"command"`
	Workdir      string `json:"workdir,omitempty"`
	SessionID    string `json:"session_id"`
	Owner        string `json:"owner,omitempty"`
	RingCapacity int    `json:"ring_capacity,omitempty"`
}
type ProcessInputPayload struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
	Owner     string `json:"owner"`
}
type ProcessOutputPayload struct {
	SessionID string `json:"session_id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Running   bool   `json:"running"`
}

type ListDirectoryPayload struct {
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
}
type FileInfoPayload struct {
	Path string `json:"path"`
}
type ReadMultipleFilesPayload struct {
	Paths []string `json:"paths"`
	Limit int64    `json:"limit,omitempty"`
}
type CreateDirectoryPayload struct {
	Path string `json:"path"`
}
type MoveFilePayload struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}
type SearchPayload struct {
	Root       string `json:"root"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}
type FileInfo struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mod_time"`
}
type SearchResult struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}
