package orchestrator

import (
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path with data through a temp file in the same
// directory, fsyncing the file and then the parent directory so a replacement
// survives a crash or power loss. An interrupted write leaves the previous file
// intact. The records written this way (state, credentials, model selection and
// a worktree ownership record) must never be observed half-written.
func writeFileAtomic(path, pattern string, data []byte) error {
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
	return syncStateFile(path, temp)
}
