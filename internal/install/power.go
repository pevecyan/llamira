package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"llamira/internal/gpu"
)

const (
	PowerFile = "/etc/llamira/power-limits.json"
	PowerUnit = "/etc/systemd/system/llamira-power.service"
)

// LoadPowerLimits returns the persisted limits (GPU index → watts).
func LoadPowerLimits() map[int]float64 {
	m := map[int]float64{}
	if b, err := os.ReadFile(PowerFile); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// setPower validates "INDEX:WATTS" or "INDEX:default", applies it and persists it so the
// boot unit reapplies it.
func setPower(ctx context.Context, spec string, out io.Writer) error {
	idxS, wattsS, ok := strings.Cut(spec, ":")
	idx, err := strconv.Atoi(idxS)
	if !ok || err != nil || idx < 0 {
		return fmt.Errorf("invalid power spec %q (want INDEX:WATTS or INDEX:default)", spec)
	}
	limits, err := gpu.PowerLimits(ctx)
	if err != nil {
		return err
	}
	var p *gpu.Power
	for i := range limits {
		if limits[i].Index == idx {
			p = &limits[i]
		}
	}
	if p == nil {
		return fmt.Errorf("no GPU %d", idx)
	}
	persisted := LoadPowerLimits()
	watts := p.Default
	if wattsS == "default" {
		delete(persisted, idx)
	} else {
		w, err := strconv.ParseFloat(wattsS, 64)
		if err != nil || w < p.Min || w > p.Max {
			return fmt.Errorf("power limit for GPU %d must be %.0f–%.0f W", idx, p.Min, p.Max)
		}
		watts = w
		persisted[idx] = w
	}
	if err := run("nvidia-smi", "-i", strconv.Itoa(idx), "-pl", fmt.Sprintf("%.0f", watts)); err != nil {
		return err
	}
	fmt.Fprintf(out, "GPU %d power limit set to %.0f W\n", idx, watts)
	b, _ := json.MarshalIndent(persisted, "", "  ")
	if err := os.WriteFile(PowerFile, b, 0o644); err != nil {
		return err
	}
	return ensurePowerUnit(len(persisted) > 0)
}

// applyPower reapplies persisted limits (boot unit).
func applyPower(ctx context.Context, out io.Writer) error {
	limits := LoadPowerLimits()
	var idx []int
	for i := range limits {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var errs []error
	for _, i := range idx {
		if err := run("nvidia-smi", "-i", strconv.Itoa(i), "-pl", fmt.Sprintf("%.0f", limits[i])); err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(out, "GPU %d power limit %.0f W\n", i, limits[i])
	}
	return errors.Join(errs...)
}

// ensurePowerUnit installs/enables (or disables) the oneshot unit that reapplies limits at boot.
func ensurePowerUnit(enable bool) error {
	if !enable {
		_ = run("systemctl", "disable", "llamira-power.service")
		return nil
	}
	unit := fmt.Sprintf(`[Unit]
Description=llamira: reapply GPU power limits
After=nvidia-persistenced.service
Before=llama-server.service

[Service]
Type=oneshot
ExecStart=%s privileged power-apply all

[Install]
WantedBy=multi-user.target
`, BinPath)
	if err := os.WriteFile(PowerUnit, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "enable", "llamira-power.service")
}

// setPowerTemporary sets "INDEX:WATTS" without persisting it (used by the power sweep).
func setPowerTemporary(ctx context.Context, spec string, out io.Writer) error {
	idxS, wattsS, ok := strings.Cut(spec, ":")
	idx, err := strconv.Atoi(idxS)
	w, werr := strconv.ParseFloat(wattsS, 64)
	if !ok || err != nil || werr != nil || idx < 0 {
		return fmt.Errorf("invalid power spec %q (want INDEX:WATTS)", spec)
	}
	limits, err := gpu.PowerLimits(ctx)
	if err != nil {
		return err
	}
	for _, p := range limits {
		if p.Index == idx {
			if w < p.Min || w > p.Max {
				return fmt.Errorf("power limit for GPU %d must be %.0f–%.0f W", idx, p.Min, p.Max)
			}
			if err := run("nvidia-smi", "-i", strconv.Itoa(idx), "-pl", fmt.Sprintf("%.0f", w)); err != nil {
				return err
			}
			fmt.Fprintf(out, "GPU %d power limit temporarily %.0f W\n", idx, w)
			return nil
		}
	}
	return fmt.Errorf("no GPU %d", idx)
}
