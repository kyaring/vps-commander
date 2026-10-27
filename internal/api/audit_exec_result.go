package api

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/wjyhk/vps-commander/internal/executor"
)

// AuditResultExitNonzero marks an exec whose command ran to completion on the
// device but returned a non-zero exit code. It is distinct from "error", which
// is reserved for failures of the Hub, the agent, or the tool call itself.
const AuditResultExitNonzero = "exit_nonzero"

// maxExecAuditMessageRunes bounds the stderr/stdout excerpt kept in audit rows.
const maxExecAuditMessageRunes = 1024

type auditCommandExitKey struct{}

// withCommandExit tags an audit context as recording a command's own exit code.
func withCommandExit(ctx context.Context) context.Context {
	return context.WithValue(ctx, auditCommandExitKey{}, true)
}

func isCommandExit(ctx context.Context) bool {
	v, _ := ctx.Value(auditCommandExitKey{}).(bool)
	return v
}

// auditResultForCode maps an exit code to the audit result for this context.
type auditResultKey struct{}

// withAuditResult overrides the audit result of one record (for example "timeout" or "killed").
func withAuditResult(ctx context.Context, result string) context.Context {
	return context.WithValue(ctx, auditResultKey{}, result)
}

func auditResultOverride(ctx context.Context) string {
	v, _ := ctx.Value(auditResultKey{}).(string)
	return v
}

func auditResultForCode(ctx context.Context, code int) string {
	if r := auditResultOverride(ctx); r != "" {
		return r
	}
	if isCommandExit(ctx) {
		if code == 0 {
			return "ok"
		}
		return AuditResultExitNonzero
	}
	if code == 0 || (code >= 200 && code <= 299) {
		return "ok"
	}
	return "error"
}

// execAuditMessage keeps the tail of stderr. When a command fails with an
// empty stderr (go test prints failures to stdout), it keeps the tail of
// stdout instead so the audit row still explains the failure.
func execAuditMessage(res executor.Result) string {
	if msg := strings.TrimSpace(res.Stderr); msg != "" {
		return tailRunes(msg, maxExecAuditMessageRunes)
	}
	if res.ExitCode == 0 {
		return ""
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		return "stdout: " + tailRunes(out, maxExecAuditMessageRunes)
	}
	return ""
}

func tailRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-max+1:])
}
