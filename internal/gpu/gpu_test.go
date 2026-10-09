package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	gs, err := parse("0, NVIDIA GeForce RTX 3060, GPU-aaa, 12288, 5321, 87, 64, 151.20, 170.00, 1837, 55\n" +
		"1, NVIDIA GeForce RTX 3060, GPU-bbb, 12288, 4700, 0, 41, 18.5, 170.00, 210, [N/A]\n")
	if err != nil || len(gs) != 2 {
		t.Fatalf("%v %+v", err, gs)
	}
	g := gs[0]
	if g.Vendor != "nvidia" || g.MemUsedMiB != 5321 || g.UtilPct != 87 || g.PowerW != 151.2 || g.ClockSM != 1837 || g.FanPct != 55 {
		t.Errorf("gpu0 %+v", g)
	}
	if gs[1].FanPct != -1 {
		t.Errorf("fan [N/A] should be -1: %+v", gs[1])
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadAMD(t *testing.T) {
	root := t.TempDir()
	// card1: an AMD GPU with VRAM info; card0: Intel iGPU; card1-DP-1: a connector
	dev := filepath.Join(root, "card1", "device")
	write(t, filepath.Join(dev, "vendor"), "0x1002\n")
	write(t, filepath.Join(dev, "product_name"), "Radeon RX 7900 XTX\n")
	write(t, filepath.Join(dev, "uevent"), "DRIVER=amdgpu\nPCI_SLOT_NAME=0000:03:00.0\n")
	write(t, filepath.Join(dev, "mem_info_vram_total"), "25753026560\n")
	write(t, filepath.Join(dev, "mem_info_vram_used"), "1073741824\n")
	write(t, filepath.Join(dev, "gpu_busy_percent"), "42\n")
	write(t, filepath.Join(dev, "pp_dpm_sclk"), "0: 500Mhz\n1: 2482Mhz *\n")
	hw := filepath.Join(dev, "hwmon", "hwmon3")
	write(t, filepath.Join(hw, "temp1_input"), "61000\n")
	write(t, filepath.Join(hw, "power1_average"), "212000000\n")
	write(t, filepath.Join(hw, "power1_cap"), "339000000\n")
	write(t, filepath.Join(hw, "pwm1"), "128\n")
	write(t, filepath.Join(root, "card0", "device", "vendor"), "0x8086\n")
	write(t, filepath.Join(root, "card1-DP-1", "status"), "connected\n")

	gs, err := readAMD(root)
	if err != nil || len(gs) != 1 {
		t.Fatalf("%v %+v", err, gs)
	}
	g := gs[0]
	want := GPU{Index: 0, Vendor: "amd", Name: "Radeon RX 7900 XTX", UUID: "amd-0000:03:00.0", MemTotalMiB: 24560, MemUsedMiB: 1024,
		UtilPct: 42, TempC: 61, PowerW: 212, PowerLimitW: 339, ClockSM: 2482, FanPct: 50}
	if g != want {
		t.Errorf("got  %+v\nwant %+v", g, want)
	}
}
