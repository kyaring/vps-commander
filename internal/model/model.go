package model

import "github.com/wjyhk/vps-commander/internal/sysinfo"

type ExecRequest struct {
	Device  string `json:"device,omitempty"`
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type ExecResponse struct {
	Device     string `json:"device"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
}

type Device struct {
	Name    string           `json:"name"`
	Status  string           `json:"status"`
	Local   bool             `json:"is_local"`
	Arch    string           `json:"arch,omitempty"`
	OS      string           `json:"os,omitempty"`
	Profile *sysinfo.Profile `json:"profile,omitempty"`
}

type FileReadRequest struct {
	Device string `json:"device,omitempty"`
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
}

type FileReadResponse struct {
	Device    string `json:"device"`
	Path      string `json:"path"`
	Offset    int64  `json:"offset"`
	Content   string `json:"content"`
	Bytes     int64  `json:"bytes"`
	TotalSize int64  `json:"total_size"`
	HasMore   bool   `json:"has_more"`
	EOF       bool   `json:"eof,omitempty"`
}

type FileWriteRequest struct {
	Device  string `json:"device,omitempty"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileWriteResponse struct {
	Device string `json:"device"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
}
