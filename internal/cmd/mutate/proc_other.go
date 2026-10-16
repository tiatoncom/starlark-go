//go:build !unix

package main

import (
	"os"
	"os/exec"
)

func setProcessGroup(cmd *exec.Cmd) {}

// killGroup kills the process only: the measure by group (ps) is a unix one.
func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
