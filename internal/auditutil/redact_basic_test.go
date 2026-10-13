package auditutil

import (
	"strings"
	"testing"
)

func TestRedactAuthorizationSchemeCredentials(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       string
		credential string
	}{
		{
			name:       "Authorization Basic",
			input:      "Authorization: Basic dXNlcjpwYXNz",
			want:       "Authorization: Basic [REDACTED]",
			credential: "dXNlcjpwYXNz",
		},
		{
			name:       "quoted Authorization Basic",
			input:      `-H "Authorization: Basic dXNlcjpwYXNz"`,
			want:       `-H "Authorization: Basic [REDACTED]"`,
			credential: "dXNlcjpwYXNz",
		},
		{
			name:       "Proxy-Authorization basic",
			input:      "Proxy-Authorization: basic abc",
			want:       "Proxy-Authorization: basic [REDACTED]",
			credential: "abc",
		},
		{
			name:       "authorization NTLM assignment",
			input:      "authorization=NTLM abc",
			want:       "authorization=NTLM [REDACTED]",
			credential: "abc",
		},
		{
			name:       "BASIC uppercase",
			input:      "Authorization: BASIC secret1",
			want:       "Authorization: BASIC [REDACTED]",
			credential: "secret1",
		},
		{
			name:       "Basic mixed case",
			input:      "Authorization: Basic secret2",
			want:       "Authorization: Basic [REDACTED]",
			credential: "secret2",
		},
		{
			name:       "basic lowercase",
			input:      "Authorization: basic secret3",
			want:       "Authorization: basic [REDACTED]",
			credential: "secret3",
		},
		{
			name:       "Negotiate",
			input:      "Authorization: Negotiate negotiate-secret",
			want:       "Authorization: Negotiate [REDACTED]",
			credential: "negotiate-secret",
		},
		{
			name:       "Token scheme",
			input:      "Authorization: Token token-secret",
			want:       "Authorization: Token [REDACTED]",
			credential: "token-secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.input)
			if got != tt.want {
				t.Fatalf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if n := strings.Count(got, "[REDACTED]"); n != 1 {
				t.Fatalf("redaction marker count = %d, want 1: %q", n, got)
			}
			if strings.Contains(got, tt.credential) {
				t.Fatalf("credential %q remained in result %q", tt.credential, got)
			}
		})
	}
}

func TestRedactBasicSchemeRegression(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "Authorization: Bearer abc123", want: "Authorization: Bearer [REDACTED]"},
		{input: "Bearer abc123", want: "Bearer [REDACTED]"},
		{input: "Authorization: abc123", want: "Authorization: [REDACTED]"},
		{input: "Authorization: Basic", want: "Authorization: [REDACTED]"},
		{input: "token=abc", want: "token=[REDACTED]"},
		{input: "api_key: abc", want: "api_key: [REDACTED]"},
		{input: "password=abc", want: "password=[REDACTED]"},
		{input: "cookie: a=b", want: "cookie: [REDACTED]"},
		{input: "bearer: abc", want: "bearer: [REDACTED]"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Redact(tt.input); got != tt.want {
				t.Fatalf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
