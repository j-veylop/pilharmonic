package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AcquireInstanceLock prevents a second pilharmonic process from using the
// same state file at the same time. Two writers would race their atomic
// replacements and silently lose whichever state file was renamed first, so
// the CLI launcher takes this lock before opening the service. The lock is
// optional for callers that manage a single instance themselves, such as
// tests that reopen a state file after shutting the previous service down.
// The returned release function is safe to call more than once.
func AcquireInstanceLock(statePath string) (func(), error) {
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
