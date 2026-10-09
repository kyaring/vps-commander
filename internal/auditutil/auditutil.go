// Package auditutil provides shared audit redaction and command truncation.
package auditutil

import (
	"regexp"
	"strings"
)

var bearerSecretRE = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)([^\s,;]+)`)
var secretRE = regexp.MustCompile(`(?i)(authorization|bearer|token|api[_-]?key|password|secret|cookie)(\s*[:=]\s*)([^\s,;]+)`)

// Redact removes common secret assignments before any length limit is applied.
func Redact(value string) string {
	value = bearerSecretRE.ReplaceAllString(value, `$1[REDACTED]`)
	value = secretRE.ReplaceAllString(value, `$1$2[REDACTED]`)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 8192 {
		value = value[:8192]
	}
	return value
}

// Truncate limits audit intents to 512 Unicode code points.
func Truncate(value string) string {
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:511]) + "…"
	}
	return value
}
