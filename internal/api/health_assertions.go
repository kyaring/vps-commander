package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	healthAssertionsMaxBytes = 1536
	healthAssertionsMaxCount = 32
	healthIntegrityCacheTTL  = 60 * time.Second
)

type healthAssertion struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type healthAssertionSummary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
}

type healthAssertionsResponse struct {
	GeneratedAt string                 `json:"generated_at"`
	Summary     healthAssertionSummary `json:"summary"`
	Assertions  []healthAssertion      `json:"assertions"`
}

type healthAssertionProbes struct {
	serviceState func() (active bool, restarts int, err error)
	internalHTTP func() error
	publicHTTP   func() error
}

func (s *Server) requireAnyScope(scopes []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		for _, scope := range scopes {
			if p.HasScope(scope) {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"devices:read or admin scope required"}`))
	})
}

func (s *Server) healthAssertions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	probes := healthAssertionProbes{
		serviceState: systemdServiceState,
		internalHTTP: func() error {
			rawURL, err := internalHealthzURLFromArgs(os.Args)
			if err != nil {
				return err
			}
			return checkHealthz(rawURL)
		},
		publicHTTP: checkPublicHealthz,
	}
	result := s.collectHealthAssertions(probes)
	body, err := json.Marshal(result)
	if err != nil || len(body) > healthAssertionsMaxBytes {
		// Details are intentionally fixed, short, and non-sensitive. This is a
		// final guard against accidentally expanding the public response contract.
		result = compactHealthAssertions(result)
		body, err = json.Marshal(result)
	}
	if err != nil || len(body)+1 > healthAssertionsMaxBytes {
		// Keep the endpoint available even if a future detail unexpectedly grows.
		result = healthAssertionsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Assertions: make([]healthAssertion, 0, 9)}
		for _, id := range []string{"service.active", "service.restarts", "hub.healthz_internal", "hub.healthz_public", "notification.legacy_retired", "notification.no_plaintext", "notification.routes", "notification.deliveries", "db.integrity"} {
			result.Assertions = append(result.Assertions, healthAssertion{ID: id, OK: false, Detail: "response limit"})
			result.Summary.Fail++
		}
		body, _ = json.Marshal(result)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}

func (s *Server) collectHealthAssertions(probes healthAssertionProbes) healthAssertionsResponse {
	items := make([]healthAssertion, 0, 9)
	active, restarts, serviceErr := probes.serviceState()
	if serviceErr != nil {
		items = append(items,
			healthAssertion{ID: "service.active", OK: false, Detail: "service state unavailable"},
			healthAssertion{ID: "service.restarts", OK: false, Detail: "restart count unavailable"},
		)
	} else {
		items = append(items, healthAssertion{ID: "service.active", OK: active, Detail: map[bool]string{true: "active", false: "inactive"}[active]})
		items = append(items, healthAssertion{ID: "service.restarts", OK: restarts == 0, Detail: "NRestarts=" + strconv.Itoa(restarts) + " restarts"})
	}
	items = append(items, assertionFromProbe("hub.healthz_internal", probes.internalHTTP, "HTTP 200", "health check unavailable"))
	items = append(items, assertionFromProbe("hub.healthz_public", probes.publicHTTP, "HTTP 200", "public health check unavailable"))

	legacyCount, legacyErr := s.countLegacyNotRetired()
	if legacyErr != nil {
		items = append(items, healthAssertion{ID: "notification.legacy_retired", OK: false, Detail: "query failed"})
	} else {
		items = append(items, healthAssertion{ID: "notification.legacy_retired", OK: legacyCount == 0, Detail: strconv.Itoa(legacyCount) + " active legacy channels or targets"})
	}

	endpointCount, targetCount, plaintextErr := s.countPlaintextEndpoints()
	if plaintextErr != nil {
		items = append(items, healthAssertion{ID: "notification.no_plaintext", OK: false, Detail: "query failed"})
	} else {
		ok := endpointCount == 0 && targetCount == 0
		items = append(items, healthAssertion{ID: "notification.no_plaintext", OK: ok, Detail: strconv.Itoa(endpointCount) + " configured URLs; " + strconv.Itoa(targetCount) + " legacy targets"})
	}

	routes, routesErr := s.countEnabledRoutes()
	if routesErr != nil {
		items = append(items, healthAssertion{ID: "notification.routes", OK: false, Detail: "query failed"})
	} else {
		items = append(items, healthAssertion{ID: "notification.routes", OK: true, Detail: strconv.Itoa(routes) + " enabled routes"})
	}

	pending, failed, deliveryErr := s.countDeliveries()
	if deliveryErr != nil {
		items = append(items, healthAssertion{ID: "notification.deliveries", OK: false, Detail: "query failed"})
	} else {
		items = append(items, healthAssertion{ID: "notification.deliveries", OK: failed == 0, Detail: strconv.Itoa(pending) + " pending; " + strconv.Itoa(failed) + " failed deliveries"})
	}

	integrityOK, integrityDetail := s.cachedIntegrityCheck()
	items = append(items, healthAssertion{ID: "db.integrity", OK: integrityOK, Detail: integrityDetail})
	if len(items) > healthAssertionsMaxCount {
		items = items[:healthAssertionsMaxCount]
	}
	result := healthAssertionsResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Assertions: items}
	for _, item := range items {
		if item.OK {
			result.Summary.Pass++
		} else {
			result.Summary.Fail++
		}
	}
	return result
}

func assertionFromProbe(id string, probe func() error, passDetail, failDetail string) healthAssertion {
	if probe == nil {
		return healthAssertion{ID: id, OK: false, Detail: failDetail}
	}
	if err := probe(); err != nil {
		return healthAssertion{ID: id, OK: false, Detail: failDetail}
	}
	return healthAssertion{ID: id, OK: true, Detail: passDetail}
}

func systemdServiceState() (bool, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "vps-commander-hub.service", "--property=ActiveState", "--property=NRestarts").Output()
	if err != nil {
		return false, 0, err
	}
	return parseSystemdServiceState(string(out))
}

func parseSystemdServiceState(output string) (bool, int, error) {
	var activeState string
	var restartsValue string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return false, 0, sql.ErrNoRows
		}
		switch key {
		case "ActiveState":
			if activeState != "" || value == "" {
				return false, 0, sql.ErrNoRows
			}
			activeState = value
		case "NRestarts":
			if restartsValue != "" || value == "" {
				return false, 0, sql.ErrNoRows
			}
			restartsValue = value
		}
	}
	validStates := map[string]bool{
		"active": true, "reloading": true, "inactive": true,
		"failed": true, "activating": true, "deactivating": true,
		"maintenance": true,
	}
	if !validStates[activeState] || restartsValue == "" {
		return false, 0, sql.ErrNoRows
	}
	restarts, err := strconv.Atoi(restartsValue)
	if err != nil || restarts < 0 {
		return false, 0, sql.ErrNoRows
	}
	return activeState == "active", restarts, nil
}

func internalHealthzURLFromArgs(args []string) (string, error) {
	var addr string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "-addr" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", sql.ErrNoRows
			}
			addr = strings.TrimSpace(args[i+1])
			break
		}
		if strings.HasPrefix(arg, "-addr=") {
			addr = strings.TrimSpace(strings.TrimPrefix(arg, "-addr="))
			break
		}
	}
	if addr == "" {
		return "", sql.ErrNoRows
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return "", sql.ErrNoRows
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", sql.ErrNoRows
	}
	return (&url.URL{Scheme: "http", Host: addr, Path: "/healthz"}).String(), nil
}

const healthzCheckTimeout = 5 * time.Second

func checkHealthz(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), healthzCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: healthzCheckTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sql.ErrNoRows
	}
	return nil
}

func checkPublicHealthz() error {
	raw := strings.TrimSpace(os.Getenv("VPS_COMMANDER_PUBLIC_URL"))
	if raw == "" {
		return sql.ErrNoRows
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
		return sql.ErrNoRows
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return sql.ErrNoRows
	}
	return checkHealthz(u.Scheme + "://" + u.Host + "/healthz")
}

func (s *Server) countLegacyNotRetired() (int, error) {
	if s.Store == nil || s.Store.DB == nil {
		return 0, sql.ErrConnDone
	}
	var channels, targets int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM notification_channels WHERE COALESCE(legacy_webhook_id,'')<>'' AND (enabled<>0 OR COALESCE(endpoint,'')<>'')`).Scan(&channels); err != nil {
		return 0, err
	}
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM webhook_targets`).Scan(&targets); err != nil {
		return 0, err
	}
	return channels + targets, nil
}

func (s *Server) countPlaintextEndpoints() (int, int, error) {
	if s.Store == nil || s.Store.DB == nil {
		return 0, 0, sql.ErrConnDone
	}
	var endpoints, targets int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM notification_channels WHERE COALESCE(endpoint,'')<>''`).Scan(&endpoints); err != nil {
		return 0, 0, err
	}
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM webhook_targets`).Scan(&targets); err != nil {
		return 0, 0, err
	}
	return endpoints, targets, nil
}

func (s *Server) countEnabledRoutes() (int, error) {
	if s.Store == nil || s.Store.DB == nil {
		return 0, sql.ErrConnDone
	}
	var count int
	err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM notification_tasks WHERE enabled<>0`).Scan(&count)
	return count, err
}

func (s *Server) countDeliveries() (int, int, error) {
	if s.Store == nil || s.Store.DB == nil {
		return 0, 0, sql.ErrConnDone
	}
	var pending, failed int
	err := s.Store.DB.QueryRow(`SELECT COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0) FROM notification_deliveries`).Scan(&pending, &failed)
	return pending, failed, err
}

func (s *Server) cachedIntegrityCheck() (bool, string) {
	s.healthIntegrityMu.Lock()
	defer s.healthIntegrityMu.Unlock()
	if s.healthIntegrityCached && time.Since(s.healthIntegrityAt) < healthIntegrityCacheTTL {
		return s.healthIntegrityOK, s.healthIntegrityDetail
	}
	ok, detail := false, "database unavailable"
	if s.Store != nil && s.Store.DB != nil {
		var result string
		err := s.Store.DB.QueryRow(`PRAGMA integrity_check`).Scan(&result)
		if err == nil && result == "ok" {
			ok, detail = true, "ok"
		} else if err == nil {
			detail = "integrity check failed"
		} else {
			detail = "integrity check unavailable"
		}
	}
	s.healthIntegrityAt = time.Now()
	s.healthIntegrityOK = ok
	s.healthIntegrityDetail = detail
	s.healthIntegrityCached = true
	return ok, detail
}

func compactHealthAssertions(in healthAssertionsResponse) healthAssertionsResponse {
	for i := range in.Assertions {
		if len(in.Assertions[i].Detail) > 48 {
			in.Assertions[i].Detail = in.Assertions[i].Detail[:48]
		}
	}
	return in
}
