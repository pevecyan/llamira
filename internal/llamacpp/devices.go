package llamacpp

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Device is a compute device as llama.cpp names it (CUDA0, ROCm0, Vulkan0, …).
type Device struct {
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	TotalMiB int    `json:"total_mib"`
}

var reDevice = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*?\d+): (.*?) \((\d+) MiB, \d+ MiB free\)`)

// ParseDevices parses `llama-server --list-devices`.
func ParseDevices(out string) []Device {
	var ds []Device
	for _, l := range strings.Split(out, "\n") {
		if m := reDevice.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[3])
			ds = append(ds, Device{Name: m[1], Desc: m[2], TotalMiB: n})
		}
	}
	return ds
}

// ListDevices runs `binary --list-devices` (initialising the GPU backend takes a second or two).
func ListDevices(ctx context.Context, binary, workDir string, env []string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--list-devices")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	return ParseDevices(string(out)), nil
}
