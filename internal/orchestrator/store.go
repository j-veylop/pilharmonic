package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

var (
	errShuttingDown = errors.New("service is shutting down")
	errTaskNotFound = errors.New("task not found")
)

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// IDs also name worktree reservations; collision-prone IDs in durable
		// state are worse than failing loudly.
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// loadState reads the state file. A corrupt file is preserved beside it and the
// service starts empty with a warning; a newer format is refused.
func (s *Service) loadState() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &s.data); err != nil {
		corrupt := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if renameErr := os.Rename(s.path, corrupt); renameErr != nil {
			return fmt.Errorf("read state: %w (preserving corrupt file failed: %v)", err, renameErr)
		}
		s.startupWarning = fmt.Sprintf("state file was corrupt (%v); preserved as %s and started empty", err, corrupt)
		s.data = state{}
	} else if s.data.Version > stateVersion {
		return fmt.Errorf("state file version %d is newer than this build supports (%d)", s.data.Version, stateVersion)
	}
	s.data.Version = stateVersion
	return nil
}

func (s *Service) save() error {
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	if err = writeFileAtomic(s.path, ".state-*", b); err != nil {
		return err
	}
	// A successful write also persisted any output the writer only buffered.
	s.outputPending = 0
	s.persistErr = nil
	return nil
}

// writeFileAtomic replaces path through a temp file in the same directory,
// fsyncing the file and the directory, so state, credentials, model selection
// and ownership records are never observed half-written.
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

// taskLocked returns the task with id, or nil. Caller holds mu.
func (s *Service) taskLocked(id string) *Task {
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			return &s.data.Tasks[i]
		}
	}
	return nil
}

func (s *Service) threadLocked(id string) *Thread {
	for i := range s.data.Threads {
		if s.data.Threads[i].ID == id {
			return &s.data.Threads[i]
		}
	}
	return nil
}

// withTask runs fn under mu on the named task, refusing once shutdown began.
func (s *Service) withTask(id string, fn func(*Task) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errShuttingDown
	}
	t := s.taskLocked(id)
	if t == nil {
		return errTaskNotFound
	}
	return fn(t)
}

// whileOpen runs fn under mu unless the service is shutting down.
func (s *Service) whileOpen(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errShuttingDown
	}
	return fn()
}

// updateTask applies fn to the task and persists the result; see
// mutateTaskLocked.
func (s *Service) updateTask(id string, fn func(*Task) error) error {
	return s.withTask(id, func(t *Task) error { return s.mutateTaskLocked(t, fn) })
}

// mutateTaskLocked applies fn and persists the task, restoring the previous
// value if either step fails, so a rejected transition is never half-applied.
// Caller holds mu.
func (s *Service) mutateTaskLocked(t *Task, fn func(*Task) error) error {
	old := clone(*t)
	if err := fn(t); err != nil {
		*t = old
		return err
	}
	if err := s.save(); err != nil {
		*t = old
		return err
	}
	return nil
}

// commitLocked persists changes made to tasks and threads since snapshot was
// taken, restoring the snapshot when the save fails. The snapshot copies task
// and thread values, so it undoes field changes and appends, but not in-place
// edits of nested slice elements. Pointers into the lists are stale after a
// restore. Caller holds mu.
func (s *Service) commitLocked(snapshot state) error {
	if err := s.save(); err != nil {
		s.data.Tasks, s.data.Threads = snapshot.Tasks, snapshot.Threads
		return err
	}
	return nil
}

func (s *Service) snapshotLocked() state {
	return state{Tasks: slices.Clone(s.data.Tasks), Threads: slices.Clone(s.data.Threads)}
}

// PersistenceError returns the most recent background save failure, or nil.
func (s *Service) PersistenceError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}

// StartupWarning reports a non-fatal problem opening the state file.
func (s *Service) StartupWarning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startupWarning
}
