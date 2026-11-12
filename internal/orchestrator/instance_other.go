//go:build !unix

package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// lockInstance falls back to an exclusive create on platforms without flock.
// The lock file names its owning process so a file left behind by a crash can be
// reclaimed instead of blocking every later launch.
func lockInstance(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		// The file may be stale. Reclaiming it is best effort: another process
		// can claim the path between the liveness probe and the removal, and the
		// retried exclusive create below loses that race rather than overriding
		// a live owner.
		if !reclaimStaleLock(path) {
			return nil, err
		}
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err != nil {
			return nil, err
		}
	}
	if _, err = f.WriteString(fmt.Sprintf("%d\n", os.Getpid())); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = os.Remove(path) })
	}, nil
}

// reclaimStaleLock removes the lock file when the PID it records is not running.
func reclaimStaleLock(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || processAlive(pid) {
		// An unreadable record or a live owner keeps the lock where it is.
		return false
	}
	return os.Remove(path) == nil
}

// processAlive probes a PID without affecting it. Signal 0 is the portable
// liveness check, but Windows does not implement it and reports an error even
// for a running process, so there a successful handle open is the best evidence
// available. Otherwise an unclear result keeps the existing lock: a stale file
// the operator must remove is safer than two writers on one state file.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// Windows and most ports fail here for a PID that is not running.
		return false
	}
	defer func() { _ = proc.Release() }()
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) {
		return false
	}
	return runtime.GOOS == "windows"
}
