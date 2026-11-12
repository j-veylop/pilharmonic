//go:build unix

package orchestrator

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// lockInstance takes an advisory exclusive flock on the lock file. A stale
// lock file left by a crashed process is harmless: the kernel drops the
// flock at process exit, so a new instance can take it again even though the
// file still exists.
func lockInstance(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Truncate(0); err == nil {
		_, err = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	if err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}
