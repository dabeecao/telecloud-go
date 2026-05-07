//go:build windows

package tgclient

import (
	"os/exec"
)

func setProcessGroup(cmd *exec.Cmd) {
	// On Windows, the default behavior of exec.CommandContext 
	// is generally enough to handle process termination.
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
