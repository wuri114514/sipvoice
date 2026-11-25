// Package sysinfo 采集服务器运行指标，供控制台探针展示。
package sysinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"time"
)

// Proc 是某个进程的资源占用。
type Proc struct {
	PID   int     `json:"pid"`
	RSSKB uint64  `json:"rss_kb"`
	CPU   float64 `json:"cpu"`
}

// Snapshot 是一次采样。
type Snapshot struct {
	At        int64           `json:"at"`
	CPU       float64         `json:"cpu"` // 0-100
	Load1     float64         `json:"load1"`
	Load5     float64         `json:"load5"`
	Load15    float64         `json:"load15"`
	MemTotal  uint64          `json:"mem_total_kb"`
	MemAvail  uint64          `json:"mem_avail_kb"`
	SwapTotal uint64          `json:"swap_total_kb"`
	SwapFree  uint64          `json:"swap_free_kb"`
	DiskTotal uint64          `json:"disk_total"`
	DiskFree  uint64          `json:"disk_free"`
	Uptime    int64           `json:"uptime_sec"`
	Temp      float64         `json:"temp_c"`
	RxRate    float64         `json:"rx_bps"`
	TxRate    float64         `json:"tx_bps"`
	Procs     map[string]Proc `json:"procs"`
	Cores     int             `json:"cores"`
}

var (
	mu                  sync.Mutex
	prevIdle, prevTotal uint64
	prevAt              time.Time
	prevRx, prevTx      uint64
	prevNetAt           time.Time
	history             []Snapshot
)

// Collect 采集一次指标（内部保存上一次计数器用于算速率/占用率）。
func Collect() Snapshot {
	mu.Lock()
	defer mu.Unlock()

	s := Snapshot{At: time.Now().Unix(), Procs: map[string]Proc{}}
	s.Cores = runtimeCores()
	s.CPU, prevIdle, prevTotal = cpuPercent(prevIdle, prevTotal)
	if l := readLoad(); len(l) >= 3 {
		s.Load1, s.Load5, s.Load15 = l[0], l[1], l[2]
	}
	s.MemTotal, s.MemAvail = readMem()
	s.SwapTotal, s.SwapFree = readSwap()
	s.DiskTotal, s.DiskFree = disk()
	s.Uptime = readUptime()
	s.Temp = readTemp()
	s.RxRate, s.TxRate, prevRx, prevTx = netRate(prevRx, prevTx)
	s.Procs = procStats([]string{"asterisk", "voicebridge"})

	history = append(history, s)
	if len(history) > 120 {
		history = history[len(history)-120:]
	}
	return s
}

// History 返回最近采样。
func History() []Snapshot {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Snapshot, len(history))
	copy(out, history)
	return out
}

func runtimeCores() int {
	// 不引入 runtime 依赖：读 /sys 或 /proc/cpuinfo
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "processor\t")
}

func cpuPercent(prevIdle, prevTotal uint64) (float64, uint64, uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, prevIdle, prevTotal
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, prevIdle, prevTotal
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, prevIdle, prevTotal
	}
	var total, idle uint64
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	if prevTotal == 0 || total <= prevTotal {
		return 0, idle, total
	}
	dTotal := total - prevTotal
	dIdle := idle - prevIdle
	pct := 100 * float64(dTotal-dIdle) / float64(dTotal)
	return pct, idle, total
}

func readLoad() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return nil
	}
	var out []float64
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseFloat(f[i], 64)
		out = append(out, v)
	}
	return out
}

func readMem() (total, avail uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	return
}

func readSwap() (total, free uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "SwapTotal:":
			total = v
		case "SwapFree:":
			free = v
		}
	}
	return
}

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

func readTemp() float64 {
	matches, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err == nil && v > 0 {
			return v / 1000 // millidegree
		}
	}
	return 0
}

func netRate(prevRx, prevTx uint64) (rxRate float64, txRate float64, curRx uint64, curTx uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, prevRx, prevTx
	}
	defer f.Close()
	var rx, tx uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.Contains(line, ":") || strings.HasPrefix(line, "lo:") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	now := time.Now()
	if prevNetAt.IsZero() || now.Sub(prevNetAt) < time.Second {
		prevNetAt = now
		return 0, 0, rx, tx
	}
	sec := now.Sub(prevNetAt).Seconds()
	rxRate = float64(rx-prevRx) / sec
	txRate = float64(tx-prevTx) / sec
	prevNetAt = now
	return rxRate, txRate, rx, tx
}

func procStats(names []string) map[string]Proc {
	out := map[string]Proc{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		matched := ""
		for _, n := range names {
			if name == n || strings.HasPrefix(name, n) {
				matched = n
				break
			}
		}
		if matched == "" {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		var rss uint64
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					rss, _ = strconv.ParseUint(f[1], 10, 64)
				}
			}
		}
		out[matched] = Proc{PID: pid, RSSKB: rss}
	}
	return out
}
