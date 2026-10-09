// Package gpu reads GPU telemetry: NVIDIA via nvidia-smi (no cgo/NVML needed), AMD from the
// amdgpu sysfs interface. Power limits and per-process memory are NVIDIA-only.
package gpu

import (
	"context"
	"encoding/csv"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type GPU struct {
	Index       int     `json:"index"`
	Vendor      string  `json:"vendor"` // nvidia, amd
	Name        string  `json:"name"`
	UUID        string  `json:"uuid"`
	MemTotalMiB int     `json:"mem_total_mib"`
	MemUsedMiB  int     `json:"mem_used_mib"`
	UtilPct     int     `json:"util_pct"`
	TempC       int     `json:"temp_c"`
	PowerW      float64 `json:"power_w"`
	PowerLimitW float64 `json:"power_limit_w"`
	ClockSM     int     `json:"clock_sm_mhz"`
	FanPct      int     `json:"fan_pct"` // -1 if not reported
}

const query = "index,name,uuid,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,power.limit,clocks.sm,fan.speed"

// Read returns the host's GPUs: NVIDIA (nvidia-smi) if available, otherwise AMD (sysfs).
func Read(ctx context.Context) ([]GPU, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu="+query, "--format=csv,noheader,nounits").Output()
	if err == nil {
		return parse(string(out))
	}
	if amd, aerr := readAMD(SysfsRoot); aerr == nil && len(amd) > 0 {
		return amd, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNoGPU
	}
	return nil, err
}

func parse(out string) ([]GPU, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var gpus []GPU
	for _, f := range recs {
		if len(f) < 9 {
			continue
		}
		gpus = append(gpus, GPU{
			Index:       atoi(f[0]),
			Vendor:      "nvidia",
			Name:        f[1],
			UUID:        f[2],
			MemTotalMiB: atoi(f[3]),
			MemUsedMiB:  atoi(f[4]),
			UtilPct:     atoi(f[5]),
			TempC:       atoi(f[6]),
			PowerW:      atof(f[7]),
			PowerLimitW: atof(f[8]),
			ClockSM:     atoiOr(f, 9, 0),
			FanPct:      atoiOr(f, 10, -1),
		})
	}
	return gpus, nil
}

func atoiOr(f []string, i, def int) int {
	if i >= len(f) {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(f[i]))
	if err != nil {
		return def // "[N/A]"
	}
	return n
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return n
}

// App is a process using GPU memory.
type App struct {
	GPUUUID string `json:"gpu_uuid"`
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	UsedMiB int    `json:"used_mib"`
}

// Apps lists compute processes and their GPU memory.
func Apps(ctx context.Context) ([]App, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-compute-apps=gpu_uuid,pid,process_name,used_memory",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var apps []App
	for _, f := range recs {
		if len(f) == 4 {
			apps = append(apps, App{GPUUUID: f[0], PID: atoi(f[1]), Name: f[2], UsedMiB: atoi(f[3])})
		}
	}
	return apps, nil
}

// ReservedMiB returns, per GPU index, memory used by anything other than processes whose
// name contains exclude (e.g. "llama-server"): driver overhead and other applications.
func ReservedMiB(gpus []GPU, apps []App, exclude string) map[int]int {
	res := map[int]int{}
	for _, g := range gpus {
		used := g.MemUsedMiB
		for _, a := range apps {
			if a.GPUUUID == g.UUID && strings.Contains(a.Name, exclude) {
				used -= a.UsedMiB
			}
		}
		res[g.Index] = max(used, 0)
	}
	return res
}

// Power is a GPU's power-limit range.
type Power struct {
	Index   int     `json:"index"`
	Limit   float64 `json:"limit_w"`
	Default float64 `json:"default_w"`
	Min     float64 `json:"min_w"`
	Max     float64 `json:"max_w"`
}

// PowerLimits reads the current, default and allowed power limits.
func PowerLimits(ctx context.Context) ([]Power, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,power.limit,power.default_limit,power.min_limit,power.max_limit",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var ps []Power
	for _, f := range recs {
		if len(f) == 5 {
			ps = append(ps, Power{Index: atoi(f[0]), Limit: atof(f[1]), Default: atof(f[2]), Min: atof(f[3]), Max: atof(f[4])})
		}
	}
	return ps, nil
}
