package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Profile struct {
	Role            string  `json:"role"`
	CPUCores        int     `json:"cpu_cores"`
	CPUUsagePercent int     `json:"cpu_usage_percent"`
	MemPercent      int     `json:"mem_percent"`
	MemAvailable    string  `json:"mem_available"`
	SwapPercent     int     `json:"swap_percent"`
	DiskPercent     int     `json:"disk_percent"`
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
	memPct, memAvl, swapPct := getMemAndSwap()
	diskPct := getDiskPercent()
	psi := getIOPSI()
	docker := hasDocker()

	return Profile{
		Role:            role,
		CPUCores:        cores,
		CPUUsagePercent: cpuPercent,
		MemPercent:      memPct,
		MemAvailable:    memAvl,
		SwapPercent:     swapPct,
		DiskPercent:     diskPct,
		IOPSI:           psi,
		Docker:          docker,
		UpdatedAt:       time.Now().Unix(),
	}
}

func getCPUUsage() int {
	// 尝试从 /proc/stat 快速采样或 /proc/loadavg
	pct := sampleCPUStat()
	if pct >= 0 {
		return pct
	}
	// 回退到 loadavg / cores * 100
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

func getMemAndSwap() (memPct int, memAvl string, swapPct int) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, "N/A", 0
	}
	defer f.Close()

	var total, free, avail, buffers, cached, swapTotal, swapFree uint64
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
		case "SwapTotal":
			swapTotal = v
		case "SwapFree":
			swapFree = v
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

	if swapTotal > 0 {
		swapUsed := swapTotal - swapFree
		swapPct = int(float64(swapUsed) / float64(swapTotal) * 100)
	}

	return memPct, memAvl, swapPct
}

func getDiskPercent() int {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	if total == 0 {
		return 0
	}
	used := total - free
	pct := int(float64(used) / float64(total) * 100)
	if pct > 100 {
		pct = 100
	}
	return pct
}

func getIOPSI() float64 {
	// 读取 /proc/pressure/io
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
	// 回退：/proc/loadavg 的 1min load
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
