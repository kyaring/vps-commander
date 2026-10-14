package auth

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

// principalTestRequest 构造一个 GET 请求，可带 Authorization 头。
// RemoteAddr 固定为 203.0.113.7:5555（TEST-NET-3 文档地址），保证 ClientIP 结果确定。
func principalTestRequest(authorization string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

// TestHasScope 对应任务卡第二节第 1 条。
func TestHasScope(t *testing.T) {
	tests := []struct {
		name  string
		p     Principal
		scope string
		want  bool
	}{
		{"panel type allows any scope", Principal{Type: "panel"}, "exec", true},
		{"panel type allows scope outside map", Principal{Type: "panel", Scopes: map[string]bool{"files:read": true}}, "exec", true},
		{"system type allows any scope", Principal{Type: "system"}, "exec", true},
		{"api_token with granted scope", Principal{Type: "api_token", Scopes: map[string]bool{"exec": true}}, "exec", true},
		{"api_token with scope set to false", Principal{Type: "api_token", Scopes: map[string]bool{"exec": false}}, "exec", false},
		{"api_token without the scope", Principal{Type: "api_token", Scopes: map[string]bool{"files:read": true}}, "exec", false},
		{"api_token with nil scopes", Principal{Type: "api_token"}, "exec", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.HasScope(tc.scope); got != tc.want {
				t.Fatalf("HasScope(%q) = %v, want %v", tc.scope, got, tc.want)
			}
		})
	}
}

// TestAllowsDevice 对应任务卡第二节第 2 条。
func TestAllowsDevice(t *testing.T) {
	tests := []struct {
		name   string
		p      Principal
		device string
		want   bool
	}{
		{"nil devices allows any device", Principal{Type: "api_token"}, "node-01", true},
		{"empty devices map allows any device", Principal{Type: "api_token", Devices: map[string]bool{}}, "node-01", true},
		{"listed device granted", Principal{Type: "api_token", Devices: map[string]bool{"node-01": true}}, "node-01", true},
		{"device not listed", Principal{Type: "api_token", Devices: map[string]bool{"node-01": true}}, "node-02", false},
		{"listed device with false value", Principal{Type: "api_token", Devices: map[string]bool{"node-01": false}}, "node-01", false},
		{"device name is case sensitive", Principal{Type: "api_token", Devices: map[string]bool{"node-01": true}}, "NODE-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.AllowsDevice(tc.device); got != tc.want {
				t.Fatalf("AllowsDevice(%q) = %v, want %v", tc.device, got, tc.want)
			}
		})
	}
}

// TestExpired 对应任务卡第二节第 3 条。
func TestExpired(t *testing.T) {
	now := time.Now().Unix()
	past := now - 10
	future := now + 3600
	tests := []struct {
		name string
		at   *int64
		want bool
	}{
		{"nil timestamp never expires", nil, false},
		{"timestamp 10 seconds in the past", &past, true},
		{"timestamp equal to current second", &now, true},
		{"timestamp 3600 seconds in the future", &future, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Expired(tc.at); got != tc.want {
				t.Fatalf("Expired() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPrincipalFromRequestLegacy 对应任务卡第二节第 4 条（旧版令牌模式）。
func TestPrincipalFromRequestLegacy(t *testing.T) {
	const token = "secret-a" // 假令牌，仅用于测试
	tests := []struct {
		name          string
		managerToken  string
		authorization string
		wantOK        bool
		wantType      string
		wantID        string
		wantAdmin     bool
	}{
		{"missing authorization header", token, "", false, "", "", false},
		{"valid bearer token", token, "Bearer " + token, true, "api_token", "legacy-env", true},
		{"wrong bearer token", token, "Bearer wrong-token", false, "", "", false},
		{"empty manager token rejects any bearer", "", "Bearer " + token, false, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(tc.managerToken)
			p, ok := m.PrincipalFromRequest(principalTestRequest(tc.authorization))
			if ok != tc.wantOK {
				t.Fatalf("PrincipalFromRequest ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if p.Type != tc.wantType || p.ID != tc.wantID {
				t.Fatalf("principal Type = %q, ID = %q; want Type = %q, ID = %q", p.Type, p.ID, tc.wantType, tc.wantID)
			}
			if got := p.Scopes["admin"]; got != tc.wantAdmin {
				t.Fatalf("principal Scopes[admin] = %v, want %v", got, tc.wantAdmin)
			}
		})
	}
}

// TestPrincipalFromRequestValidator 对应任务卡第二节第 5 条（校验器模式）。
func TestPrincipalFromRequestValidator(t *testing.T) {
	const legacyToken = "secret-a"         // 假令牌：仅用于验证校验器优先、不回落旧版令牌
	const requestToken = "validator-token" // 假令牌：请求里携带的值

	t.Run("validator ok returns validator principal", func(t *testing.T) {
		want := Principal{Type: "api_token", ID: "api-id-1", Name: "named-token-1", TokenID: "api-id-1", Scopes: map[string]bool{"exec": true}}
		m := NewManagerWithValidator(legacyToken, TokenValidatorFunc(func(token, ip string) (Principal, bool) {
			return want, true
		}))
		got, ok := m.PrincipalFromRequest(principalTestRequest("Bearer " + requestToken))
		if !ok {
			t.Fatal("PrincipalFromRequest ok = false, want true")
		}
		if got.ID != want.ID || got.Name != want.Name {
			t.Fatalf("principal ID = %q, Name = %q; want ID = %q, Name = %q", got.ID, got.Name, want.ID, want.Name)
		}
	})

	t.Run("validator rejects without legacy fallback", func(t *testing.T) {
		m := NewManagerWithValidator(legacyToken, TokenValidatorFunc(func(token, ip string) (Principal, bool) {
			return Principal{}, false
		}))
		// 请求令牌与构造时的旧版令牌相同；校验器返回 false 时必须整体失败
		if _, ok := m.PrincipalFromRequest(principalTestRequest("Bearer " + legacyToken)); ok {
			t.Fatal("PrincipalFromRequest ok = true, want false: validator must take precedence over the legacy token")
		}
	})

	t.Run("validator receives request token and client ip", func(t *testing.T) {
		var gotToken, gotIP string
		m := NewManagerWithValidator(legacyToken, TokenValidatorFunc(func(token, ip string) (Principal, bool) {
			gotToken, gotIP = token, ip
			return Principal{Type: "api_token", ID: "api-id-2"}, true
		}))
		r := principalTestRequest("Bearer " + requestToken)
		if _, ok := m.PrincipalFromRequest(r); !ok {
			t.Fatal("PrincipalFromRequest ok = false, want true")
		}
		if gotToken != requestToken {
			t.Fatalf("validator token = %q, want %q", gotToken, requestToken)
		}
		if want := ClientIP(r); gotIP != want {
			t.Fatalf("validator ip = %q, want ClientIP(r) = %q", gotIP, want)
		}
	})
}

// TestRotate 对应任务卡第二节第 6 条。
func TestRotate(t *testing.T) {
	hex48 := regexp.MustCompile(`^[0-9a-f]{48}$`)
	m := NewManager("secret-a") // 假初始令牌

	first := m.Rotate()
	if !hex48.MatchString(first) {
		t.Fatalf("Rotate() = %q, want 48 lowercase hex characters", first)
	}
	if got := m.Token(); got != first {
		t.Fatalf("Token() = %q after Rotate(), want %q", got, first)
	}

	second := m.Rotate()
	if !hex48.MatchString(second) {
		t.Fatalf("second Rotate() = %q, want 48 lowercase hex characters", second)
	}
	if second == first {
		t.Fatal("two consecutive Rotate() calls returned the same token")
	}

	// 轮换掉的就令牌不能再通过 PrincipalFromRequest
	if _, ok := m.PrincipalFromRequest(principalTestRequest("Bearer " + first)); ok {
		t.Fatal("PrincipalFromRequest ok = true for the rotated-away token, want false")
	}
}
