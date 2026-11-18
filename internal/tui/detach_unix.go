//go:build unix

package tui

import (
	"os/exec"
	"syscall"
)

// detach puts a launcher in its own session, so closing the terminal that runs
// the board does not hang up the terminal it opened.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
