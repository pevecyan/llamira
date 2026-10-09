// Package hoststat reads host RAM, swap, CPU and load from procfs, CPU temperature from hwmon,
// and the memory and CPU of the llama-server unit from its cgroup (or its processes), without
// any extra tool. A Sampler keeps the latest snapshot; CPU percentages are deltas between
// samples.
package hoststat

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Paths are the filesystem roots (variables for tests).
type Paths struct {
	Proc   string // /proc
	Sys    string // /sys
	Cgroup string // /sys/fs/cgroup
}

var DefaultPaths = Paths{Proc: "/proc", Sys: "/sys", Cgroup: "/sys/fs/cgroup"}

// Host is one snapshot.
type Host struct {
	Time      time.Time  `json:"time"`
	MemTotal  uint64     `json:"mem_total"` // bytes
	MemUsed   uint64     `json:"mem_used"`  // total − available
	MemAvail  uint64     `json:"mem_available"`
	SwapTotal uint64     `json:"swap_total"`
	SwapUsed  uint64     `json:"swap_used"`
	Cores     int        `json:"cores"`
	CPUPct    float64    `json:"cpu_pct"`  // all cores, 0..100
	PerCore   []float64  `json:"per_core"` // 0..100 per core
	Load      [3]float64 `json:"load"`     // 1, 5, 15 min
	CPUTempC  float64    `json:"cpu_temp_c,omitempty"`
	Unit      *Unit      `json:"unit,omitempty"` // the llama-server unit (router + model processes)
}

// Unit is the memory and CPU of a systemd unit.
type Unit struct {
	Name   string  `json:"name"`
	Source string  `json:"source"`  // cgroup or proc
	Mem    uint64  `json:"mem"`     // bytes: memory.current (cgroup) or summed RSS (proc)
	Anon   uint64  `json:"anon"`    // anonymous (KV cache, compute buffers, CPU weights when not mmap'd)
	File   uint64  `json:"file"`    // file-backed (mmap'd weights, page cache)
	CPUPct float64 `json:"cpu_pct"` // of all cores, 0..100
	PIDs   int     `json:"pids,omitempty"`
}

// ---- procfs -------------------------------------------------------------------------------

// meminfo returns /proc/meminfo values in bytes.
func meminfo(proc string) (map[string]uint64, error) {
	f, err := os.Open(filepath.Join(proc, "meminfo"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) == 0 {
			continue
		}
		v, _ := strconv.ParseUint(fs[0], 10, 64)
		if len(fs) > 1 && fs[1] == "kB" {
			v <<= 10
		}
		m[k] = v
	}
	return m, sc.Err()
}

// cpuTimes is one /proc/stat cpu line: busy and total jiffies.
type cpuTimes struct{ busy, total uint64 }

// stat returns the aggregate and per-core CPU times.
func stat(proc string) (all cpuTimes, cores []cpuTimes, err error) {
	b, err := os.ReadFile(filepath.Join(proc, "stat"))
	if err != nil {
		return all, nil, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		fs := strings.Fields(l)
		if len(fs) < 5 || !strings.HasPrefix(fs[0], "cpu") {
			continue
		}
		var t cpuTimes
		for i, f := range fs[1:] {
			v, _ := strconv.ParseUint(f, 10, 64)
			if i == 7 || i == 8 { // guest, guest_nice are already in user/nice
				continue
			}
			t.total += v
			if i != 3 && i != 4 { // idle, iowait
				t.busy += v
			}
		}
		if fs[0] == "cpu" {
			all = t
		} else {
			cores = append(cores, t)
		}
	}
	return all, cores, nil
}

func pct(a, b cpuTimes) float64 {
	dt := float64(b.total - a.total)
	if b.total <= a.total || dt == 0 {
		return 0
	}
	return clamp(float64(b.busy-a.busy) / dt * 100)
}

func clamp(v float64) float64 { return max(0, min(100, v)) }

func loadavg(proc string) [3]float64 {
	var l [3]float64
	b, err := os.ReadFile(filepath.Join(proc, "loadavg"))
	if err != nil {
		return l
	}
	fs := strings.Fields(string(b))
	for i := 0; i < 3 && i < len(fs); i++ {
		l[i], _ = strconv.ParseFloat(fs[i], 64)
	}
	return l
}

// cpuTemp reads the CPU package temperature from hwmon (k10temp / zenpower for AMD, coretemp
// for Intel); 0 if there is none.
func cpuTemp(sys string) float64 {
	dirs, _ := filepath.Glob(filepath.Join(sys, "class", "hwmon", "hwmon*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		name := strings.TrimSpace(readFile(filepath.Join(d, "name")))
		switch name {
		case "k10temp", "zenpower", "coretemp", "cpu_thermal":
		default:
			continue
		}
		// prefer the package sensor (Tctl / Package id 0), else the first input
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		sort.Strings(inputs)
		best := ""
		for _, in := range inputs {
			label := strings.TrimSpace(readFile(strings.TrimSuffix(in, "_input") + "_label"))
			if label == "Tctl" || label == "Tdie" || strings.HasPrefix(label, "Package") {
				best = in
				break
			}
			if best == "" {
				best = in
			}
		}
		if best != "" {
			if v, err := strconv.ParseFloat(strings.TrimSpace(readFile(best)), 64); err == nil && v > 0 {
				return v / 1000
			}
		}
	}
	return 0
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// ---- unit: cgroup v2, or processes ---------------------------------------------------------

// cgroupStat reads memory and CPU usage of a cgroup directory.
func cgroupStat(dir string) (mem, anon, file, usageUsec uint64, err error) {
	if mem, err = strconv.ParseUint(strings.TrimSpace(readFile(filepath.Join(dir, "memory.current"))), 10, 64); err != nil {
		return
	}
	for _, l := range strings.Split(readFile(filepath.Join(dir, "memory.stat")), "\n") {
		k, v, _ := strings.Cut(l, " ")
		n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		switch k {
		case "anon":
			anon = n
		case "file":
			file = n
		}
	}
	for _, l := range strings.Split(readFile(filepath.Join(dir, "cpu.stat")), "\n") {
		if v, ok := strings.CutPrefix(l, "usage_usec "); ok {
			usageUsec, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	return
}

// procTree returns pid and all its descendants.
func procTree(proc string, pid int) []int {
	out := []int{pid}
	for i := 0; i < len(out) && len(out) < 1024; i++ {
		tasks, _ := filepath.Glob(filepath.Join(proc, strconv.Itoa(out[i]), "task", "*", "children"))
		for _, t := range tasks {
			for _, c := range strings.Fields(readFile(t)) {
				if n, err := strconv.Atoi(c); err == nil {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// procStat sums RSS (anon + file) and CPU ticks (utime + stime) of processes.
func procStat(proc string, pids []int) (anon, file, ticks uint64, n int) {
	for _, pid := range pids {
		base := filepath.Join(proc, strconv.Itoa(pid))
		status := readFile(filepath.Join(base, "status"))
		if status == "" {
			continue
		}
		n++
		for _, l := range strings.Split(status, "\n") {
			k, v, _ := strings.Cut(l, ":")
			fs := strings.Fields(v)
			if len(fs) == 0 {
				continue
			}
			kb, _ := strconv.ParseUint(fs[0], 10, 64)
			switch k {
			case "RssAnon":
				anon += kb << 10
			case "RssFile", "RssShmem":
				file += kb << 10
			}
		}
		// /proc/<pid>/stat: fields after the ")" of comm; utime and stime are fields 14 and 15
		st := readFile(filepath.Join(base, "stat"))
		if i := strings.LastIndexByte(st, ')'); i >= 0 {
			fs := strings.Fields(st[i+1:])
			if len(fs) > 12 {
				u, _ := strconv.ParseUint(fs[11], 10, 64)
				s, _ := strconv.ParseUint(fs[12], 10, 64)
				ticks += u + s
			}
		}
	}
	return
}

// ---- sampler ------------------------------------------------------------------------------

// UnitInfo resolves the unit's cgroup path (e.g. /system.slice/llama-server.service) and main
// PID (0 if not running).
type UnitInfo func(ctx context.Context) (cgroup string, mainPID int)

// Sampler takes a snapshot every interval and keeps the latest one.
type Sampler struct {
	Paths    Paths
	UnitName string
	Unit     UnitInfo

	mu     sync.Mutex
	latest *Host
	prev   struct {
		at        time.Time
		all       cpuTimes
		cores     []cpuTimes
		unitUsec  uint64
		unitTicks uint64
		unitSrc   string
	}
}

// clkTck is USER_HZ, which is 100 on every Linux architecture Go supports.
const clkTck = 100

// Sample takes a snapshot now (CPU % relative to the previous sample).
func (s *Sampler) Sample(ctx context.Context) (*Host, error) {
	p := s.Paths
	if p.Proc == "" {
		p = DefaultPaths
	}
	mi, err := meminfo(p.Proc)
	if err != nil {
		return nil, err
	}
	h := &Host{Time: time.Now(), MemTotal: mi["MemTotal"], SwapTotal: mi["SwapTotal"]}
	h.MemAvail = mi["MemAvailable"]
	if h.MemAvail == 0 { // very old kernels
		h.MemAvail = mi["MemFree"] + mi["Buffers"] + mi["Cached"]
	}
	h.MemUsed = h.MemTotal - min(h.MemAvail, h.MemTotal)
	h.SwapUsed = h.SwapTotal - min(mi["SwapFree"], h.SwapTotal)
	h.Load = loadavg(p.Proc)
	h.CPUTempC = cpuTemp(p.Sys)
	all, cores, err := stat(p.Proc)
	if err != nil {
		return nil, err
	}
	h.Cores = len(cores)

	var unit *Unit
	var usec, ticks uint64
	if s.Unit != nil {
		cg, pid := s.Unit(ctx)
		if cg != "" {
			if mem, anon, file, u, err := cgroupStat(filepath.Join(p.Cgroup, cg)); err == nil {
				unit = &Unit{Name: s.UnitName, Source: "cgroup", Mem: mem, Anon: anon, File: file}
				usec = u
			}
		}
		if unit == nil && pid > 0 {
			anon, file, t, n := procStat(p.Proc, procTree(p.Proc, pid))
			if n > 0 {
				unit = &Unit{Name: s.UnitName, Source: "proc", Mem: anon + file, Anon: anon, File: file, PIDs: n}
				ticks = t
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prev.at.IsZero() {
		h.CPUPct = pct(s.prev.all, all)
		if len(s.prev.cores) == len(cores) {
			h.PerCore = make([]float64, len(cores))
			for i := range cores {
				h.PerCore[i] = pct(s.prev.cores[i], cores[i])
			}
		}
		if unit != nil && unit.Source == s.prev.unitSrc && h.Cores > 0 {
			wall := h.Time.Sub(s.prev.at).Seconds() * float64(h.Cores)
			switch {
			case unit.Source == "cgroup" && usec >= s.prev.unitUsec:
				unit.CPUPct = clamp(float64(usec-s.prev.unitUsec) / 1e6 / wall * 100)
			case unit.Source == "proc" && ticks >= s.prev.unitTicks:
				unit.CPUPct = clamp(float64(ticks-s.prev.unitTicks) / clkTck / wall * 100)
			}
		}
	}
	s.prev.at, s.prev.all, s.prev.cores = h.Time, all, cores
	s.prev.unitUsec, s.prev.unitTicks, s.prev.unitSrc = usec, ticks, ""
	if unit != nil {
		s.prev.unitSrc = unit.Source
	}
	h.Unit = unit
	s.latest = h
	return h, nil
}

// Run samples every interval until ctx is done.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		_, _ = s.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Latest returns the most recent snapshot (nil before the first one, or off Linux).
func (s *Sampler) Latest() *Host {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// AvailableForUnit is the RAM the unit could use: what is available now plus the unit's own
// anonymous memory (its file-backed pages are already counted as available).
func (h *Host) AvailableForUnit() uint64 {
	if h == nil {
		return 0
	}
	a := h.MemAvail
	if h.Unit != nil {
		a += h.Unit.Anon
	}
	return min(a, h.MemTotal)
}
