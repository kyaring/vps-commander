package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// toolArgKinds is the declared JSON type of every tool argument, taken from the inputSchema each tool
// publishes in tools/list. TestArgKindsMatchSchemas keeps this table and the schemas in step, so an
// argument can never be checked against a type its tool does not declare.
var toolArgKinds = map[string]string{
	"action": "string", "arguments": "object", "command": "string", "content": "string",
	"dedup_window_seconds": "integer", "destination": "string", "device": "string", "job_id": "string",
	"key": "string", "limit": "integer", "lines": "integer", "max_bytes": "integer", "new_text": "string",
	"offset": "integer", "old_text": "string", "output_mode": "string", "path": "string", "paths": "array",
	"server": "string", "source": "string", "target": "string", "timeout": "integer", "tool": "string",
	"ttl_seconds": "integer", "value": "string", "workdir": "string",
}

// validateToolArgs checks the arguments of a tools/call request against the declared argument types.
// An absent or null argument is fine. A wrong type, or arguments that are not a JSON object, is an
// error, so the call is refused instead of running with silently zeroed values. The error names the
// argument and the expected type only; it never repeats the value the caller sent.
func validateToolArgs(raw json.RawMessage) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(t, &obj); err != nil {
		return errors.New("invalid arguments: arguments must be a JSON object")
	}
	for name, value := range obj {
		kind, known := toolArgKinds[name]
		if !known {
			continue
		}
		if err := checkArgKind(kind, value); err != nil {
			return fmt.Errorf("invalid arguments: %s must be %s", name, kind)
		}
	}
	return nil
}

func checkArgKind(kind string, value json.RawMessage) error {
	v := bytes.TrimSpace(value)
	if bytes.Equal(v, []byte("null")) {
		return nil
	}
	switch kind {
	case "string":
		var s string
		return json.Unmarshal(v, &s)
	case "integer":
		var n json.Number
		if err := json.Unmarshal(v, &n); err != nil {
			return err
		}
		_, err := n.Int64()
		return err
	case "object":
		var m map[string]json.RawMessage
		return json.Unmarshal(v, &m)
	case "array":
		var a []json.RawMessage
		return json.Unmarshal(v, &a)
	}
	return fmt.Errorf("unknown kind %s", kind)
}
