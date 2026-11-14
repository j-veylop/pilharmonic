// Package instance keeps one process per state file and replaces files
// atomically, so concurrent writers cannot lose a state file.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire prevents a second pilharmonic process from using the same state
// file at the same time. Two writers would race their atomic
// replacements and silently lose whichever state file was renamed first, so
// the CLI launcher takes this lock before opening the service. The lock is
// optional for callers that manage a single instance themselves, such as
// tests that reopen a state file after shutting the previous service down.
// The returned release function is safe to call more than once.
func Acquire(statePath string) (func(), error) {
	if statePath == "" {
		return nil, errors.New("state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return nil, err
	}
	release, err := lockInstance(statePath + ".lock")
	if err != nil {
		return nil, fmt.Errorf("state file %s is already in use by another instance: %w", statePath, err)
	}
	return release, nil
}

// WriteFileAtomic replaces path through a temp file in the same directory,
// fsyncing the file and the directory, so state, credentials, model selection
// and ownership records are never observed half-written.
func WriteFileAtomic(path, pattern string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() { _ = os.Remove(temp) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	dirErr := dir.Sync()
	closeErr := dir.Close()
	// Some filesystems cannot fsync a directory. The rename already replaced
	// the file, so rolling back in-memory state would be wrong.
	if dirErr != nil && !errors.Is(dirErr, syscall.EINVAL) && !errors.Is(dirErr, syscall.ENOTSUP) {
		return dirErr
	}
	return closeErr
}
