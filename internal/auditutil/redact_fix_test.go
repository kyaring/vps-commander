package auditutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactStandaloneBearerTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "lowercase", input: "Bearer abc123", want: "Bearer [REDACTED]"},
		{name: "lower", input: "bearer abc123", want: "bearer [REDACTED]"},
		{name: "uppercase", input: "BEARER abc123", want: "BEARER [REDACTED]"},
		{name: "double quotes", input: `-H "Bearer abc123"`, want: `-H "Bearer [REDACTED]"`},
		{name: "single quotes", input: "-H 'Bearer abc123'", want: "-H 'Bearer [REDACTED]'"},
		{name: "sentence", input: "token is Bearer abc123, ok", want: "token is Bearer [REDACTED], ok"},
		{name: "semicolon", input: "Bearer abc123;next", want: "Bearer [REDACTED];next"},
		{name: "multiple", input: "Bearer first and bearer second", want: "Bearer [REDACTED] and bearer [REDACTED]"},
		{name: "already redacted", input: "Bearer [REDACTED]", want: "Bearer [REDACTED]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.input)
			if got != tt.want {
				t.Fatalf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if strings.Contains(got, "abc123") || strings.Contains(got, "first") || strings.Contains(got, "second") {
				t.Fatalf("secret token remained in result: %q", got)
			}
		})
	}
}

func TestRedactBearerWordBoundary(t *testing.T) {
	got := Redact("xbearer abc123")
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if got != "xbearer abc123" {
		t.Fatalf("word without boundary unexpectedly changed: %q", got)
	}
}

func TestRedactAuthorizationBearerExactlyOnce(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "Authorization: Bearer abc123", want: "Authorization: Bearer [REDACTED]"},
		{input: "authorization=bearer abc123", want: "authorization=bearer [REDACTED]"},
		{input: `-H "Authorization: Bearer abc"`, want: `-H "Authorization: Bearer [REDACTED]"`},
	}
	for _, tt := range tests {
		got := Redact(tt.input)
		if got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
		}
		if n := strings.Count(got, "[REDACTED]"); n != 1 {
			t.Errorf("Redact(%q) has %d redaction markers, want 1: %q", tt.input, n, got)
		}
	}
}

func TestRedactLegacySecretAssignments(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "token=x", want: "token=[REDACTED]"},
		{input: "api_key: x", want: "api_key: [REDACTED]"},
		{input: "password=x", want: "password=[REDACTED]"},
		{input: "cookie: a=b", want: "cookie: [REDACTED]"},
		{input: "bearer: x", want: "bearer: [REDACTED]"},
	}
	for _, tt := range tests {
		if got := Redact(tt.input); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedactTruncatesAtUTF8Boundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "three byte Han character", input: strings.Repeat("a", 8191) + "中", want: 8191},
		{name: "four byte emoji", input: strings.Repeat("a", 8190) + "😀", want: 8190},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.input)
			if !utf8.ValidString(got) {
				t.Fatal("result is not valid UTF-8")
			}
			if len(got) != tt.want {
				t.Fatalf("result length = %d, want %d", len(got), tt.want)
			}
			if len(got) > 8192 {
				t.Fatalf("result length = %d, exceeds 8192 bytes", len(got))
			}
		})
	}
}

func TestRedactASCIIByteLimit(t *testing.T) {
	atLimit := strings.Repeat("a", 8192)
	if got := Redact(atLimit); got != atLimit {
		t.Fatal("exactly 8192 ASCII bytes should be preserved")
	}
	overLimit := strings.Repeat("a", 8193)
	got := Redact(overLimit)
	if len(got) != 8192 {
		t.Fatalf("result length = %d, want 8192", len(got))
	}
	if got != overLimit[:8192] {
		t.Fatal("8193 ASCII bytes should be truncated to the first 8192 bytes")
	}
}

func TestTruncateBehaviorUnchanged(t *testing.T) {
	if got, want := Truncate(strings.Repeat("汉", 513)), strings.Repeat("汉", 511)+"…"; got != want {
		t.Fatalf("Truncate(513 Han characters) differs from expected output")
	}
	input := strings.Repeat("汉", 512)
	if got := Truncate(input); got != input {
		t.Fatal("Truncate should preserve exactly 512 characters")
	}
}

func TestRedactReplacesCarriageReturnAndNewline(t *testing.T) {
	if got, want := Redact("one\rtwo\nthree"), "one two three"; got != want {
		t.Fatalf("Redact CR/LF = %q, want %q", got, want)
	}
}
