package gpu

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SysfsRoot is where AMD GPUs are read from (a variable for tests).
var SysfsRoot = "/sys/class/drm"

var reCard = regexp.MustCompile(`^card(\d+)$`)

// ErrNoGPU means neither nvidia-smi nor an amdgpu device was found.
var ErrNoGPU = errors.New("no GPU found (nvidia-smi not available, no amdgpu device in /sys/class/drm)")

// readAMD reads amdgpu devices from sysfs: VRAM, busy %, temperature, power, power cap, SM
// clock and fan, without any vendor tool. Index is the order of the cards.
func readAMD(root string) ([]GPU, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	type card struct {
		n   int
		dev string
	}
	var cards []card
	for _, e := range entries {
		m := reCard.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		dev := filepath.Join(root, e.Name(), "device")
		if strings.TrimSpace(readFile(filepath.Join(dev, "vendor"))) != "0x1002" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dev, "mem_info_vram_total")); err != nil {
			continue // an AMD display-only device without amdgpu VRAM info
		}
		n, _ := strconv.Atoi(m[1])
		cards = append(cards, card{n, dev})
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].n < cards[j].n })
	var out []GPU
	for i, c := range cards {
		g := GPU{Index: i, Vendor: "amd", FanPct: -1}
		g.Name = strings.TrimSpace(readFile(filepath.Join(c.dev, "product_name")))
		if g.Name == "" {
			g.Name = "AMD GPU " + strings.TrimSpace(readFile(filepath.Join(c.dev, "device")))
		}
		g.UUID = strings.TrimSpace(readFile(filepath.Join(c.dev, "unique_id")))
		if g.UUID == "" {
			g.UUID = "amd-" + uevent(c.dev, "PCI_SLOT_NAME")
		}
		g.MemTotalMiB = int(readInt(filepath.Join(c.dev, "mem_info_vram_total")) >> 20)
		g.MemUsedMiB = int(readInt(filepath.Join(c.dev, "mem_info_vram_used")) >> 20)
		g.UtilPct = int(readInt(filepath.Join(c.dev, "gpu_busy_percent")))
		g.ClockSM = currentClock(readFile(filepath.Join(c.dev, "pp_dpm_sclk")))
		if hw := hwmon(c.dev); hw != "" {
			g.TempC = int(readInt(filepath.Join(hw, "temp1_input")) / 1000)
			p := readInt(filepath.Join(hw, "power1_average"))
			if p == 0 {
				p = readInt(filepath.Join(hw, "power1_input"))
			}
			g.PowerW = float64(p) / 1e6
			g.PowerLimitW = float64(readInt(filepath.Join(hw, "power1_cap"))) / 1e6
			if pwm, err := os.ReadFile(filepath.Join(hw, "pwm1")); err == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(string(pwm))); err == nil {
					g.FanPct = v * 100 / 255
				}
			}
		}
		out = append(out, g)
	}
	return out, nil
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func readInt(p string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(readFile(p)), 10, 64)
	return n
}

func hwmon(dev string) string {
	m, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*"))
	sort.Strings(m)
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func uevent(dev, key string) string {
	for _, l := range strings.Split(readFile(filepath.Join(dev, "uevent")), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// currentClock parses pp_dpm_sclk ("0: 500Mhz\n1: 2100Mhz *") and returns the active MHz.
func currentClock(s string) int {
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasSuffix(strings.TrimSpace(l), "*") {
			continue
		}
		_, rest, _ := strings.Cut(l, ":")
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "*"))
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(rest), "Mhz"), "MHz"))
		return n
	}
	return 0
}
