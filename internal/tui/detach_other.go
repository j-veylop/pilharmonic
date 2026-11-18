//go:build !unix

package tui

import "os/exec"

// detach is a no-op where the process model has no session to leave.
func detach(*exec.Cmd) {}
