// Package system wraps systemd and journald for the llama-server service.
package system

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ServiceStatus is a subset of `systemctl show` properties.
type ServiceStatus struct {
	Unit        string `json:"unit"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	MainPID     string `json:"main_pid"`
	Since       string `json:"since"`
	ExecStart   string `json:"exec_start"`
	DropIns     string `json:"drop_ins"`
}

// Status returns the unit's state. It does not need privileges.
func Status(ctx context.Context, unit string) (ServiceStatus, error) {
	props, err := Show(ctx, unit, "ActiveState", "SubState", "MainPID", "ActiveEnterTimestamp", "ExecStart", "DropInPaths")
	if err != nil {
		return ServiceStatus{Unit: unit}, err
	}
	return ServiceStatus{
		Unit:        unit,
		ActiveState: props["ActiveState"],
		SubState:    props["SubState"],
		MainPID:     props["MainPID"],
		Since:       props["ActiveEnterTimestamp"],
		ExecStart:   execArgv(props["ExecStart"]),
		DropIns:     props["DropInPaths"],
	}, nil
}

// Show returns `systemctl show -p ...` properties.
func Show(ctx context.Context, unit string, props ...string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"show", unit}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show %s: %w", unit, err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

// execArgv extracts "argv[]=..." from systemd's ExecStart property.
func execArgv(s string) string {
	_, rest, ok := strings.Cut(s, "argv[]=")
	if !ok {
		return s
	}
	argv, _, _ := strings.Cut(rest, " ;")
	return argv
}

// UnitEnv returns the Environment= entries of a unit (e.g. LLAMA_CACHE=..., LD_LIBRARY_PATH=...).
func UnitEnv(ctx context.Context, unit string) ([]string, error) {
	props, err := Show(ctx, unit, "Environment")
	if err != nil {
		return nil, err
	}
	return strings.Fields(props["Environment"]), nil
}

// Restart restarts a unit. As a non-root user it goes through `sudo -n` (see the sudoers rule
// written by `llamira install`).
func Restart(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, "systemctl", "restart", unit)
	} else {
		cmd = exec.CommandContext(ctx, "sudo", "-n", "/usr/bin/systemctl", "restart", unit)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restart %s: %v: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Journal streams journal lines for a unit: the last n lines, then (if follow) new lines until
// ctx is done.
func Journal(ctx context.Context, unit string, n int, follow bool, fn func(string)) error {
	args := []string{"-u", unit, "-n", fmt.Sprint(n), "--no-pager", "-o", "short-iso"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
	return cmd.Wait()
}

// IsActive reports whether a unit is active (or activating). It asks list-units, not show: show
// loads a unit that is not in memory, and for a finished transient unit (a build job, polled by
// the UI) systemd then logs "Failed to open /run/systemd/transient/…" on every call. An active
// unit is always loaded, so list-units never misses one.
func IsActive(ctx context.Context, unit string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--full", "--", unit).Output()
	if err != nil {
		return false
	}
	return listedActive(string(out), unit)
}

// listedActive reads a unit's ACTIVE column from `systemctl list-units --plain --no-legend`
// output (UNIT LOAD ACTIVE SUB DESCRIPTION).
func listedActive(out, unit string) bool {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == unit {
			return f[2] == "active" || f[2] == "activating" || f[2] == "reloading"
		}
	}
	return false
}

// Privileged runs `sudo -n llamira privileged ARGS...` (or directly as root).
func Privileged(ctx context.Context, bin string, args ...string) (string, error) {
	full := append([]string{"privileged"}, args...)
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, bin, full...)
	} else {
		cmd = exec.CommandContext(ctx, "sudo", append([]string{"-n", bin}, full...)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
