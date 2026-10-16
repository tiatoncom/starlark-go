//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a group of its own, so that the whole
// group can be measured and killed.
func setProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }
