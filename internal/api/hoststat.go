package api

import (
	"context"
	"strconv"
	"sync"
	"time"

	"llamira/internal/hoststat"
	"llamira/internal/system"
)

// UnitInfo returns a hoststat.UnitInfo for a systemd unit: its cgroup path and main PID,
// re-read every 30 s (they change when the unit restarts).
func UnitInfo(unit string) hoststat.UnitInfo {
	var mu sync.Mutex
	var at time.Time
	var cg string
	var pid int
	return func(ctx context.Context) (string, int) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < 30*time.Second {
			return cg, pid
		}
		at = time.Now()
		if props, err := system.Show(ctx, unit, "ControlGroup", "MainPID"); err == nil {
			cg = props["ControlGroup"]
			pid, _ = strconv.Atoi(props["MainPID"])
		}
		return cg, pid
	}
}
