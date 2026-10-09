package cluster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandshakeLimiterPerIPBurstAndRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newHandshakeLimiter(now)
	for i := 0; i < int(handshakeIPBurst); i++ {
		if !l.Allow("192.0.2.1", now) {
			t.Fatalf("request %d within burst was denied", i+1)
		}
	}
	if l.Allow("192.0.2.1", now) {
		t.Fatal("request beyond per-IP burst was allowed")
	}
	if !l.Allow("192.0.2.2", now) {
		t.Fatal("different IP should have independent bucket")
	}
	if !l.Allow("192.0.2.1", now.Add(time.Second)) {
		t.Fatal("bucket did not refill")
	}
}

func TestHandshakeLimiterGlobalBurst(t *testing.T) {
	now := time.Unix(2000, 0)
	l := newHandshakeLimiter(now)
	for i := 0; i < int(handshakeGlobalBurst); i++ {
		if !l.Allow(fmt.Sprintf("198.51.100.%d", i+1), now) {
			t.Fatalf("global request %d within burst was denied", i+1)
		}
	}
	if l.Allow("203.0.113.1", now) {
		t.Fatal("request beyond global burst was allowed")
	}
}

func TestServeWSLimiterIgnoresSpoofedXFFWhenProxyTrustDisabled(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "off")
	m := NewManager("valid-secret", nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", m.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < int(handshakeIPBurst); i++ {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=xff-spoof-test", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Every request claims a different public source IP. The limiter must use
		// RemoteAddr while TRUST_PROXY is disabled, not this attacker-controlled header.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d status=%d, want 401 before burst is exhausted", i+1, resp.StatusCode)
		}
	}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=xff-spoof-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.250")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF bypassed per-IP limiter: status=%d, want 429", resp.StatusCode)
	}
}

func TestServeWSLimiterUsesTrustedProxyClientIPNotLeftmostXFF(t *testing.T) {
	t.Setenv("VPS_COMMANDER_TRUST_PROXY", "on")
	m := NewManager("valid-secret", nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", m.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < int(handshakeIPBurst); i++ {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=trusted-xff-test", nil)
		if err != nil {
			t.Fatal(err)
		}
		// Model a trusted reverse proxy appending the actual peer at the right edge.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d, 203.0.113.44", i+1))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d status=%d, want 401 before burst is exhausted", i+1, resp.StatusCode)
		}
	}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=trusted-xff-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", "192.0.2.250, 203.0.113.44")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("leftmost XFF spoof bypassed trusted client IP bucket: status=%d, want 429", resp.StatusCode)
	}
}

func TestServeWSRejectsExcessiveHandshakes(t *testing.T) {
	m := NewManager("valid-secret", nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", m.ServeWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < int(handshakeIPBurst); i++ {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=throttle-test", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d status=%d, want 401", i+1, resp.StatusCode)
		}
	}
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/agent/ws?device_name=throttle-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("excess handshake status=%d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After=%q, want 1", got)
	}
}
