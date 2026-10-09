package api

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Metrics serves a compact Prometheus text exposition. Access is guarded by
// the admin scope in Routes(); callers must still pass through auth middleware.
func (s *Server) Metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	onlineAgents := 0
	if s.Cluster != nil {
		for _, device := range s.Cluster.Devices() {
			status, _ := device["status"].(string)
			isLocal, _ := device["is_local"].(bool)
			if status == "online" && !isLocal {
				onlineAgents++
			}
		}
	}
	sseSessions, streamableSessions := 0, 0
	if s.MCP != nil {
		sseSessions, streamableSessions = s.MCP.SessionCounts()
	}
	auditQueueDepth := 0
	sqliteWriteLatencySeconds := 0.0
	if s.Store != nil {
		auditQueueDepth = s.Store.AuditQueueDepth()
		sqliteWriteLatencySeconds = s.Store.LastSQLiteWriteLatency().Seconds()
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "# HELP vps_commander_online_agents Number of online remote agents.")
	fmt.Fprintln(w, "# TYPE vps_commander_online_agents gauge")
	fmt.Fprintf(w, "vps_commander_online_agents %d\n", onlineAgents)
	fmt.Fprintln(w, "# HELP vps_commander_mcp_sessions Active MCP sessions by transport.")
	fmt.Fprintln(w, "# TYPE vps_commander_mcp_sessions gauge")
	fmt.Fprintf(w, "vps_commander_mcp_sessions{transport=\"legacy_sse\"} %d\n", sseSessions)
	fmt.Fprintf(w, "vps_commander_mcp_sessions{transport=\"streamable_http\"} %d\n", streamableSessions)
	fmt.Fprintln(w, "# HELP vps_commander_in_flight_exec Number of currently executing commands.")
	fmt.Fprintln(w, "# TYPE vps_commander_in_flight_exec gauge")
	fmt.Fprintf(w, "vps_commander_in_flight_exec %d\n", s.InFlightExec())
	fmt.Fprintln(w, "# HELP vps_commander_audit_queue_depth Number of queued audit writes.")
	fmt.Fprintln(w, "# TYPE vps_commander_audit_queue_depth gauge")
	fmt.Fprintf(w, "vps_commander_audit_queue_depth %d\n", auditQueueDepth)
	fmt.Fprintln(w, "# HELP vps_commander_sqlite_write_latency_seconds Duration of the most recent SQLite audit write or batch.")
	fmt.Fprintln(w, "# TYPE vps_commander_sqlite_write_latency_seconds gauge")
	fmt.Fprintf(w, "vps_commander_sqlite_write_latency_seconds %.9f\n", sqliteWriteLatencySeconds)
	fmt.Fprintln(w, "# HELP vps_commander_http_429_total Total HTTP 429 responses observed by the Hub.")
	fmt.Fprintln(w, "# TYPE vps_commander_http_429_total counter")
	fmt.Fprintf(w, "vps_commander_http_429_total %d\n", s.HTTP429Count())
	fmt.Fprintln(w, "# HELP vps_commander_goroutines Current Go goroutine count.")
	fmt.Fprintln(w, "# TYPE vps_commander_goroutines gauge")
	fmt.Fprintf(w, "vps_commander_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintln(w, "# HELP vps_commander_process_resident_memory_bytes Resident memory size of the Hub process in bytes.")
	fmt.Fprintln(w, "# TYPE vps_commander_process_resident_memory_bytes gauge")
	fmt.Fprintf(w, "vps_commander_process_resident_memory_bytes %d\n", processRSSBytes())
}

func processRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
