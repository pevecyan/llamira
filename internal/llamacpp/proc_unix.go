//go:build unix

package llamacpp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so that it and anything it
// spawns can be stopped together.
func setProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func terminateGroup(cmd *exec.Cmd, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
