//go:build !unix

package llamacpp

import "os/exec"

// The dry-run router only runs on the llama host (Linux); these keep other builds compiling.
func setProcessGroup(cmd *exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd, kill bool) { _ = cmd.Process.Kill() }
