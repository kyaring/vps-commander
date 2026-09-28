package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var netMu sync.Mutex
var lastNet = struct {
	rx, tx uint64
	at     time.Time
}{}

type Profile struct {
	Role            string  `json:"role,omitempty"`
	CPUCores        int     `json:"cpu_cores"`
	CPUUsagePercent int     `json:"cpu_usage_percent"`
	Load1M          float64 `json:"load_1m"`
	Load5M          float64 `json:"load_5m"`
	Load15M         float64 `json:"load_15m"`
	NetRXRate       float64 `json:"net_rx_rate"`
	NetTXRate       float64 `json:"net_tx_rate"`
	NetRXTotal      uint64  `json:"net_rx_total"`
	NetTXTotal      uint64  `json:"net_tx_total"`
	UptimeSeconds   float64 `json:"uptime_seconds"`
	BootTime        int64   `json:"boot_time"`
	MemPercent      int     `json:"mem_percent"`
	MemAvailable    string  `json:"mem_available"`
	DiskPercent     int     `json:"disk_percent"`
	DiskFree        string  `json:"disk_free"`
	IOPSI           float64 `json:"io_psi"`
	Docker          bool    `json:"docker"`
	UpdatedAt       int64   `json:"updated_at"`
}

func Collect(defaultRole string) Profile {
	role := os.Getenv("VPS_COMMANDER_ROLE")
	if role == "" {
		role = defaultRole
	}
	if role == "" {
		role = "APPLICATION"
	}

	cores := runtime.NumCPU()
	cpuPercent := getCPUUsage()
	load1m, load5m, load15m := getLoads()
	netRX, netTX, netRXT, netTXT := getNetwork()
	uptime, boot := getUptime()
	memPct, memAvl := getMemInfo()
	diskPct, diskFree := getDiskInfo()
	psi := getIOPSI()
	docker := hasDocker()

	return Profile{
		Role:            role,
		CPUCores:        cores,
		CPUUsagePercent: cpuPercent,
		Load1M:          load1m,
		Load5M:          load5m,
		Load15M:         load15m,
		NetRXRate:       netRX,
		NetTXRate:       netTX,
		NetRXTotal:      netRXT,
		NetTXTotal:      netTXT,
		UptimeSeconds:   uptime,
		BootTime:        boot,
		MemPercent:      memPct,
		MemAvailable:    memAvl,
		DiskPercent:     diskPct,
		DiskFree:        diskFree,
		IOPSI:           psi,
		Docker:          docker,
		UpdatedAt:       time.Now().Unix(),
	}
}

func getLoads() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return 0, 0, 0
	}
	v := func(i int) float64 { x, _ := strconv.ParseFloat(f[i], 64); return x }
	return v(0), v(1), v(2)
}

func getNetwork() (float64, float64, uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, 0, 0
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		f := strings.Fields(parts[1])
		if len(f) < 9 {
			continue
		}
		r, e1 := strconv.ParseUint(f[0], 10, 64)
		t, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 == nil && e2 == nil {
			rx += r
			tx += t
		}
	}
	now := time.Now()
	netMu.Lock()
	defer netMu.Unlock()
	var rr, tr float64
	if !lastNet.at.IsZero() && now.After(lastNet.at) && rx >= lastNet.rx && tx >= lastNet.tx {
		d := now.Sub(lastNet.at).Seconds()
		if d > 0 {
			rr = float64(rx-lastNet.rx) / d
			tr = float64(tx-lastNet.tx) / d
		}
	}
	lastNet.rx, lastNet.tx, lastNet.at = rx, tx, now
	return rr, tr, rx, tx
}

func getUptime() (float64, int64) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, time.Now().Unix()
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, time.Now().Unix()
	}
	u, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, time.Now().Unix()
	}
	return u, time.Now().Add(-time.Duration(u * float64(time.Second))).Unix()
}

func getCPUUsage() int {
	pct := sampleCPUStat()
	if pct >= 0 {
		return pct
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if l1, err := strconv.ParseFloat(fields[0], 64); err == nil {
				cores := float64(runtime.NumCPU())
				if cores < 1 {
					cores = 1
				}
				val := int((l1 / cores) * 100)
				if val > 100 {
					val = 100
				}
				return val
			}
		}
	}
	return 0
}

func sampleCPUStat() int {
	readStat := func() (idle, total uint64, ok bool) {
		f, err := os.Open("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 5 && fields[0] == "cpu" {
				var sum uint64
				for i := 1; i < len(fields); i++ {
					v, _ := strconv.ParseUint(fields[i], 10, 64)
					sum += v
				}
				idleVal, _ := strconv.ParseUint(fields[4], 10, 64)
				return idleVal, sum, true
			}
		}
		return 0, 0, false
	}

	idle1, total1, ok1 := readStat()
	if !ok1 {
		return -1
	}
	time.Sleep(50 * time.Millisecond)
	idle2, total2, ok2 := readStat()
	if !ok2 || total2 <= total1 {
		return -1
	}
	deltaTotal := total2 - total1
	deltaIdle := idle2 - idle1
	if deltaTotal == 0 {
		return 0
	}
	busy := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
	if busy < 0 {
		busy = 0
	}
	if busy > 100 {
		busy = 100
	}
	return int(busy)
}

func getMemInfo() (memPct int, memAvl string) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, "N/A"
	}
	defer f.Close()

	var total, free, avail, buffers, cached uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}
		v, _ := strconv.ParseUint(valFields[0], 10, 64)
		switch key {
		case "MemTotal":
			total = v
		case "MemFree":
			free = v
		case "MemAvailable":
			avail = v
		case "Buffers":
			buffers = v
		case "Cached":
			cached = v
		}
	}

	if avail == 0 {
		avail = free + buffers + cached
	}

	if total > 0 {
		used := total - avail
		memPct = int(float64(used) / float64(total) * 100)
		if avail >= 1024*1024 {
			memAvl = fmt.Sprintf("%.1fG", float64(avail)/(1024*1024))
		} else {
			memAvl = fmt.Sprintf("%dM", avail/1024)
		}
	} else {
		memAvl = "N/A"
	}

	return memPct, memAvl
}

func getDiskInfo() (diskPct int, diskFree string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, "N/A"
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total == 0 {
		return 0, "N/A"
	}
	used := total - free
	diskPct = int(float64(used) / float64(total) * 100)
	if diskPct > 100 {
		diskPct = 100
	}

	if free >= 1024*1024*1024 {
		diskFree = fmt.Sprintf("%.1fG", float64(free)/(1024*1024*1024))
	} else {
		diskFree = fmt.Sprintf("%dM", free/(1024*1024))
	}

	return diskPct, diskFree
}

func getIOPSI() float64 {
	data, err := os.ReadFile("/proc/pressure/io")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "some") {
				fields := strings.Fields(line)
				for _, f := range fields {
					if strings.HasPrefix(f, "avg10=") {
						val, err := strconv.ParseFloat(strings.TrimPrefix(f, "avg10="), 64)
						if err == nil {
							return val
						}
					}
				}
			}
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if val, err := strconv.ParseFloat(fields[0], 64); err == nil {
				return val
			}
		}
	}
	return 0.0
}

func hasDocker() bool {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return true
	}
	return false
}
