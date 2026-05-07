//go:build !windows

package tgclient

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// Signal negative PID kills the process group on Unix
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
