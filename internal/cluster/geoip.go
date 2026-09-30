package cluster

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type GeoLocation struct {
	CountryCode string `json:"countryCode"`
	Country     string `json:"country"`
	Status      string `json:"status"`
}

var (
	geoCache sync.Map
	client   = &http.Client{Timeout: 4 * time.Second}
)

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

// ExtractIPFromRequest retrieves the real client IP from HTTP headers or RemoteAddr
func ExtractIPFromRequest(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		for _, p := range parts {
			clean := strings.TrimSpace(p)
			if clean != "" && !isPrivateIP(clean) {
				return clean
			}
		}
		if len(parts) > 0 {
			clean := strings.TrimSpace(parts[0])
			if clean != "" {
				return clean
			}
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// ResolveIP returns countryCode (e.g. "HK", "SG") and countryName (e.g. "Hong Kong", "Singapore")
func ResolveIP(ipStr string) (string, string) {
	ipStr = strings.TrimSpace(ipStr)
	if ipStr == "" {
		return "", ""
	}
	if isPrivateIP(ipStr) {
		return "CN", "中国"
	}
	if v, ok := geoCache.Load(ipStr); ok {
		if g, ok := v.(GeoLocation); ok {
			return g.CountryCode, g.Country
		}
	}

	url := "http://ip-api.com/json/" + ipStr + "?fields=status,country,countryCode"
	resp, err := client.Get(url)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	var g GeoLocation
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return "", ""
	}
	if g.Status == "success" && g.CountryCode != "" {
		geoCache.Store(ipStr, g)
		return g.CountryCode, g.Country
	}
	return "", ""
}

// ResolveLocalPublicIP queries the hub host's public IP location
func ResolveLocalPublicIP() (string, string) {
	if v, ok := geoCache.Load("__local__"); ok {
		if g, ok := v.(GeoLocation); ok {
			return g.CountryCode, g.Country
		}
	}
	resp, err := client.Get("http://ip-api.com/json/?fields=status,country,countryCode")
	if err != nil {
		return "HK", "中国香港"
	}
	defer resp.Body.Close()
	var g GeoLocation
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return "HK", "中国香港"
	}
	if g.Status == "success" && g.CountryCode != "" {
		geoCache.Store("__local__", g)
		return g.CountryCode, g.Country
	}
	return "HK", "中国香港"
}
