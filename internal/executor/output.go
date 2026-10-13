package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultOutputBytes = 4096
	MaxOutputBytes     = 1024 * 1024
)

type OutputOptions struct {
	Mode               string
	MaxBytes           int
	DedupWindowSeconds int
}

type ShapedOutput struct {
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Truncated  bool   `json:"truncated"`
	TotalBytes int    `json:"total_bytes"`
	TotalLines int    `json:"total_lines"`
	SHA256     string `json:"sha256"`
}

func NormalizeOutputOptions(mode string, maxBytes int) (OutputOptions, error) {
	if mode == "" {
		mode = "raw"
	}
	switch mode {
	case "raw", "tail", "head", "count", "none":
	default:
		return OutputOptions{}, fmt.Errorf("invalid output_mode: %s", mode)
	}
	if maxBytes == 0 {
		maxBytes = DefaultOutputBytes
	}
	if maxBytes < 1 || maxBytes > MaxOutputBytes {
		return OutputOptions{}, fmt.Errorf("max_bytes must be between 1 and %d", MaxOutputBytes)
	}
	return OutputOptions{Mode: mode, MaxBytes: maxBytes}, nil
}

func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// ShapeOutput applies one byte budget to the combined stdout/stderr byte stream,
// while preserving the stream fields for the retained byte ranges.
func ShapeOutput(stdout, stderr string, mode string, maxBytes int) (ShapedOutput, error) {
	opts, err := NormalizeOutputOptions(mode, maxBytes)
	if err != nil {
		return ShapedOutput{}, err
	}
	all := make([]byte, 0, len(stdout)+len(stderr))
	all = append(all, stdout...)
	all = append(all, stderr...)
	fullHash := shortHash(all)
	result := ShapedOutput{TotalBytes: len(all), TotalLines: lineCount([]byte(stdout)) + lineCount([]byte(stderr)), SHA256: fullHash, Truncated: len(all) > opts.MaxBytes}
	if opts.Mode == "count" || opts.Mode == "none" {
		return result, nil
	}
	if len(all) <= opts.MaxBytes {
		result.Stdout, result.Stderr = stdout, stderr
		return result, nil
	}
	result.Truncated = true
	start, end := 0, opts.MaxBytes
	if opts.Mode != "head" {
		start, end = len(all)-opts.MaxBytes, len(all)
	}
	kept := all[start:end]
	marker := fmt.Sprintf("[truncated: kept %d of %d bytes / %d lines; sha256=%s]\n", len(kept), len(all), result.TotalLines, fullHash)
	// Map the retained interval back onto the original stdout/stderr fields.
	stdoutStart, stdoutEnd := start, end
	if stdoutStart > len(stdout) {
		stdoutStart = len(stdout)
	}
	if stdoutEnd > len(stdout) {
		stdoutEnd = len(stdout)
	}
	if stdoutEnd > stdoutStart {
		result.Stdout = stdout[stdoutStart:stdoutEnd]
	}
	stderrStart, stderrEnd := start-len(stdout), end-len(stdout)
	if stderrStart < 0 {
		stderrStart = 0
	}
	if stderrEnd < 0 {
		stderrEnd = 0
	}
	if stderrStart > len(stderr) {
		stderrStart = len(stderr)
	}
	if stderrEnd > len(stderr) {
		stderrEnd = len(stderr)
	}
	if stderrEnd > stderrStart {
		result.Stderr = stderr[stderrStart:stderrEnd]
	}
	if result.Stdout != "" {
		result.Stdout = marker + result.Stdout
	} else {
		result.Stderr = marker + result.Stderr
	}
	return result, nil
}

// OutputDedupRecord is the compact metadata retained for a deduplicated exec.
type OutputDedupRecord struct {
	Hash          string
	FirstSeen     time.Time
	CachedBytes   int
	ExitCode      int
	WindowSeconds int
}
