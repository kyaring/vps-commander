// Package auditutil provides shared audit redaction and command truncation.
package auditutil

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var bearerTokenRE = regexp.MustCompile(`(?i)\b(bearer)(\s+)([^\s,;'"\"]+)`)
var secretRE = regexp.MustCompile(`(?i)(authorization|bearer|token|api[_-]?key|password|secret|cookie)(\s*[:=]\s*)([^\s,;]+)`)
var redactedBearerValueRE = regexp.MustCompile(`^\s+\[REDACTED\]`)
var authSchemeCredentialRE = regexp.MustCompile(`(?i)((?:proxy-)?authorization\s*[:=]\s*)(basic|negotiate|ntlm|token)(\s+)([^\s,;'"\\"]+)`)
var authSchemeNameRE = regexp.MustCompile(`(?i)^(bearer|basic|negotiate|ntlm|token)$`)

// Redact removes common secret assignments before any length limit is applied.
func Redact(value string) string {
	value = authSchemeCredentialRE.ReplaceAllString(value, `${1}${2}${3}[REDACTED]`)
	value = bearerTokenRE.ReplaceAllStringFunc(value, func(match string) string {
		parts := bearerTokenRE.FindStringSubmatch(match)
		if parts[3] == "[REDACTED]" {
			return match
		}
		return parts[1] + parts[2] + "[REDACTED]"
	})
	value = redactSecretAssignments(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 8192 {
		cut := 8192
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		value = value[:cut]
	}
	return value
}

// redactSecretAssignments keeps already-redacted authorization schemes intact
// after their credentials have been removed, while preserving legacy rules.
func redactSecretAssignments(value string) string {
	matches := secretRE.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var out strings.Builder
	out.Grow(len(value))
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		keyStart, keyEnd := match[2], match[3]
		valueStart, valueEnd := match[6], match[7]
		out.WriteString(value[last:start])
		if strings.EqualFold(value[keyStart:keyEnd], "authorization") &&
			authSchemeNameRE.MatchString(value[valueStart:valueEnd]) &&
			redactedBearerValueRE.MatchString(value[end:]) {
			out.WriteString(value[start:end])
		} else {
			out.WriteString(value[start:valueStart])
			out.WriteString("[REDACTED]")
		}
		last = end
	}
	out.WriteString(value[last:])
	return out.String()
}

// Truncate limits audit intents to 512 Unicode code points.
func Truncate(value string) string {
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:511]) + "…"
	}
	return value
}
