package hoststat

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeRoot(t *testing.T) Paths {
	root := t.TempDir()
	p := Paths{Proc: filepath.Join(root, "proc"), Sys: filepath.Join(root, "sys"), Cgroup: filepath.Join(root, "cgroup")}
	write(t, filepath.Join(p.Proc, "meminfo"), "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    6000000 kB\nSwapTotal:       4000000 kB\nSwapFree:        3000000 kB\n")
	write(t, filepath.Join(p.Proc, "loadavg"), "1.50 0.75 0.25 2/345 6789\n")
	// user nice system idle iowait irq softirq steal guest guest_nice
	write(t, filepath.Join(p.Proc, "stat"), "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0 0 0\nintr 1\n")
	hw := filepath.Join(p.Sys, "class", "hwmon", "hwmon2")
	write(t, filepath.Join(hw, "name"), "k10temp\n")
	write(t, filepath.Join(hw, "temp1_input"), "41000\n")
	write(t, filepath.Join(hw, "temp1_label"), "Tccd1\n")
	write(t, filepath.Join(hw, "temp3_input"), "55250\n")
	write(t, filepath.Join(hw, "temp3_label"), "Tctl\n")
	write(t, filepath.Join(p.Sys, "class", "hwmon", "hwmon0", "name"), "amdgpu\n")
	return p
}

func TestSampleHostAndCgroup(t *testing.T) {
	p := fakeRoot(t)
	cg := filepath.Join(p.Cgroup, "system.slice", "llama-server.service")
	write(t, filepath.Join(cg, "memory.current"), "6442450944\n")
	write(t, filepath.Join(cg, "memory.stat"), "anon 2147483648\nfile 4294967296\nkernel 1000\n")
	write(t, filepath.Join(cg, "cpu.stat"), "usage_usec 1000000\nuser_usec 900000\n")
	s := &Sampler{Paths: p, UnitName: "llama-server.service", Unit: func(context.Context) (string, int) { return "/system.slice/llama-server.service", 42 }}

	h, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.MemTotal != 16000000<<10 || h.MemUsed != 10000000<<10 || h.SwapUsed != 1000000<<10 || h.Cores != 2 {
		t.Errorf("mem/cores %+v", h)
	}
	if h.Load != [3]float64{1.5, 0.75, 0.25} || h.CPUTempC != 55.25 {
		t.Errorf("load %v temp %v (want the Tctl sensor)", h.Load, h.CPUTempC)
	}
	if h.CPUPct != 0 || h.Unit == nil || h.Unit.Source != "cgroup" || h.Unit.Anon != 2<<30 || h.Unit.File != 4<<30 {
		t.Errorf("first sample %+v unit %+v", h, h.Unit)
	}
	if got := h.AvailableForUnit(); got != 6000000<<10+2<<30 {
		t.Errorf("available for unit %d", got)
	}

	// second sample: 1000 jiffies later, 600 busy → 60 %; cpu0 busy 400/500 → 80 %
	write(t, filepath.Join(p.Proc, "stat"), "cpu  400 0 400 1200 0 0 0 0 0 0\ncpu0 250 0 250 500 0 0 0 0 0 0\ncpu1 150 0 150 700 0 0 0 0 0 0\n")
	s.prev.at = time.Now().Add(-2 * time.Second)                    // 2 s × 2 cores = 4 core-seconds
	write(t, filepath.Join(cg, "cpu.stat"), "usage_usec 3000000\n") // +2 s of CPU → 50 %
	h, _ = s.Sample(context.Background())
	if math.Abs(h.CPUPct-60) > 0.01 || len(h.PerCore) != 2 || math.Abs(h.PerCore[0]-80) > 0.01 || math.Abs(h.PerCore[1]-40) > 0.01 {
		t.Errorf("cpu %v per core %v", h.CPUPct, h.PerCore)
	}
	if math.Abs(h.Unit.CPUPct-50) > 1 {
		t.Errorf("unit cpu %v", h.Unit.CPUPct)
	}
	if s.Latest() != h {
		t.Error("Latest should be the last sample")
	}
}

func TestUnitFromProcesses(t *testing.T) {
	p := fakeRoot(t)
	// no cgroup: main pid 100 with one child 200
	write(t, filepath.Join(p.Proc, "100", "status"), "Name:\tllama-server\nRssAnon:\t  1024 kB\nRssFile:\t  2048 kB\nRssShmem:\t 0 kB\n")
	write(t, filepath.Join(p.Proc, "100", "stat"), "100 (llama-server) S 1 100 100 0 -1 0 0 0 0 0 10 5 0 0 20 0 1 0 0 0 0\n")
	write(t, filepath.Join(p.Proc, "100", "task", "100", "children"), "200 \n")
	write(t, filepath.Join(p.Proc, "200", "status"), "Name:\tllama-server\nRssAnon:\t  4096 kB\nRssFile:\t  8192 kB\n")
	write(t, filepath.Join(p.Proc, "200", "stat"), "200 (llama server) R 100 100 100 0 -1 0 0 0 0 0 100 50 0 0 20 0 1 0 0 0 0\n")
	s := &Sampler{Paths: p, UnitName: "u", Unit: func(context.Context) (string, int) { return "/missing.slice/u.service", 100 }}
	h, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u := h.Unit
	if u == nil || u.Source != "proc" || u.PIDs != 2 || u.Anon != 5120<<10 || u.File != 10240<<10 || u.Mem != u.Anon+u.File {
		t.Fatalf("unit %+v", u)
	}
	// +300 ticks (3 s CPU) over 3 s × 2 cores → 50 %
	write(t, filepath.Join(p.Proc, "200", "stat"), "200 (llama server) R 100 100 100 0 -1 0 0 0 0 0 300 150 0 0 20 0 1 0 0 0 0\n")
	s.prev.at = time.Now().Add(-3 * time.Second)
	h, _ = s.Sample(context.Background())
	if math.Abs(h.Unit.CPUPct-50) > 1 {
		t.Errorf("unit cpu %v", h.Unit.CPUPct)
	}
}
