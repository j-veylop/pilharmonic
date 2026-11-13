package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SetTaskIsolation changes the workspace choice for a queued task. A worktree is
// selected automatically only for mandates that explicitly request changes and
// when the workspace supports isolation. Read-only and ambiguous work is shared.
// An operator choice is explicit and disables later automatic changes. Worktrees
// are preserved while the task exists, including across retries and review.
func (s *Service) SetTaskIsolation(id string, enabled bool) error {
	if enabled {
		// Validation runs Git, so it must not hold the service mutex.
		workspace, err := s.taskWorkspace(id)
		if err != nil {
			return err
		}
		if !workspaceIsolatable(workspace) {
			return s.recordIsolationUnavailable(id)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		return s.mutateTaskLocked(t, func(t *Task) error {
			if t.Status != Queued || t.ScheduleRequested {
				return errors.New("isolation can only be changed on an unscheduled queued task")
			}
			if t.Isolated == enabled {
				if !t.IsolationAuto {
					return nil
				}
				t.IsolationAuto = false
				t.UpdatedAt = time.Now().UTC()
				t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", "Workspace choice confirmed by operator"})
				return nil
			}
			if t.ExecutionWorkspace != "" {
				return errors.New("prepared workspace must be retained for retries; create a new task to change isolation")
			}
			t.Isolated = enabled
			t.IsolationAuto = false
			t.UpdatedAt = time.Now().UTC()
			message := "Shared source workspace selected"
			if enabled {
				message = "Isolated committed-HEAD worktree selected; uncommitted files are excluded"
			}
			t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", message})
			return nil
		})
	}
	return errors.New("task not found")
}

// taskWorkspace reads a task's source workspace without holding the mutex while
// Git validation runs.
func (s *Service) taskWorkspace(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return "", errors.New("service is shutting down")
	}
	for _, t := range s.data.Tasks {
		if t.ID == id {
			return t.Workspace, nil
		}
	}
	return "", errors.New("task not found")
}

// recordIsolationUnavailable persists why an explicit isolation request was
// refused so the operator can see the reason in the task history.
func (s *Service) recordIsolationUnavailable(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		if err := s.mutateTaskLocked(t, func(t *Task) error {
			t.UpdatedAt = time.Now().UTC()
			t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", "Isolation unavailable: workspace is not a Git repository with a committed HEAD"})
			return nil
		}); err != nil {
			return err
		}
		return errors.New("workspace cannot be isolated: it is not a Git repository with a committed HEAD")
	}
	return errors.New("task not found")
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// A fixed locale keeps Git's diagnostics matchable by describeMergeFailure;
	// disabling the terminal prompt keeps a missing credential from blocking a
	// worker forever.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		command := "git"
		if len(args) > 0 {
			command += " " + args[0]
		}
		return "", fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// gitPath resolves a path reported by git rev-parse before comparing it with
// a reservation. Git may return a path through a symlink.
func gitPath(ctx context.Context, dir string, option string) (string, error) {
	path, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", option)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// workspaceIsolatable reports whether a task created for workspace can start in
// a dedicated worktree. Isolation is the default only when the workspace is in
// a Git repository with a committed HEAD. A non-repository, an unborn
// repository, or a missing git executable keeps the task in the shared
// workspace because no worktree can be prepared from it.
func workspaceIsolatable(workspace string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := gitOutput(ctx, workspace, "rev-parse", "--show-toplevel"); err != nil {
		return false
	}
	_, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "HEAD^{commit}")
	return err == nil
}

type workspaceOwner struct{ TaskID, Source, CommonDir, Commit string }

func readWorkspaceOwner(reservation, taskID string) (workspaceOwner, error) {
	var owner workspaceOwner
	path := filepath.Join(reservation, "owner.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return owner, errors.New("workspace reservation has no valid ownership record")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return owner, err
	}
	if err = json.Unmarshal(b, &owner); err != nil {
		return owner, fmt.Errorf("workspace ownership: %w", err)
	}
	if owner.TaskID != taskID || owner.Source == "" || owner.CommonDir == "" || owner.Commit == "" {
		return owner, errors.New("workspace ownership does not match task")
	}
	return owner, nil
}

// emptyReservation reports a reservation an interrupted attempt left without an
// ownership record and without a worktree. Recreating it loses nothing.
func emptyReservation(reservation string) bool {
	if _, err := os.Lstat(filepath.Join(reservation, "tree")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	_, err := os.Lstat(filepath.Join(reservation, "owner.json"))
	return errors.Is(err, os.ErrNotExist)
}

// writeWorkspaceOwner persists the ownership record atomically so a crash never
// leaves a truncated record that every later attempt would reject.
func writeWorkspaceOwner(reservation string, owner workspaceOwner) error {
	b, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(reservation, "owner.json"), ".owner-*.json", b)
}

// removeReservation deletes an ownership record and its directory without
// touching a worktree.
func removeReservation(reservation string) {
	_ = os.Remove(filepath.Join(reservation, "owner.json"))
	_ = os.Remove(reservation)
}

// discardBrokenWorktree removes a worktree directory and prunes the registration
// Git keeps for it, which an interrupted worktree add can leave behind.
func discardBrokenWorktree(ctx context.Context, source, tree string) error {
	if err := os.RemoveAll(tree); err != nil {
		return err
	}
	_, _ = gitOutput(ctx, source, "worktree", "prune")
	return nil
}

func addReservedWorktree(ctx context.Context, source, tree, commit string) error {
	_, err := gitOutput(ctx, source, "worktree", "add", "--detach", "--", tree, commit)
	return err
}

// checkReservedWorktree verifies that path is still the reserved worktree of the
// repository Git reports as common.
func checkReservedWorktree(ctx context.Context, path, common string) error {
	actual, err := gitPath(ctx, path, "--git-common-dir")
	if err != nil {
		return err
	}
	if actual != common {
		return errors.New("execution workspace belongs to a different repository")
	}
	top, err := gitPath(ctx, path, "--show-toplevel")
	if err != nil {
		return err
	}
	if top != path {
		return errors.New("execution workspace is not the reserved worktree")
	}
	return nil
}

const workspaceCleanupTimeout = 30 * time.Second

// cleanupTaskWorkspace removes a deleted task's dedicated worktree and
// reservation. Cleanup runs outside the service mutex because it invokes Git.
// It never touches the source workspace.
func (s *Service) cleanupTaskWorkspace(t Task) error {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	return removeTaskWorkspace(ctx, s.path, t)
}

// validTaskID reports whether id can safely name a path component under the
// workspaces directory. filepath.IsLocal rejects an empty id, ".", "..", an
// absolute path and any embedded separator.
func validTaskID(id string) bool {
	return filepath.IsLocal(id)
}

// removeTaskWorkspace validates the reservation's ownership record before
// removing anything. A missing reservation is a successful no-op, so shared
// workspace tasks and tasks that never prepared a worktree need no special
// handling.
func removeTaskWorkspace(ctx context.Context, statePath string, t Task) error {
	if statePath == "" || !validTaskID(t.ID) {
		return errors.New("invalid task identity for workspace cleanup")
	}
	base, err := filepath.Abs(filepath.Join(filepath.Dir(statePath), "workspaces"))
	if err != nil {
		return err
	}
	reservation := filepath.Join(base, t.ID)
	info, err := os.Lstat(reservation)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace reservation: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace reservation is not a regular directory")
	}
	owner, err := readWorkspaceOwner(reservation, t.ID)
	if err != nil {
		return err
	}

	tree := filepath.Join(reservation, "tree")
	info, err = os.Lstat(tree)
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(reservation)
	}
	if err != nil {
		return fmt.Errorf("workspace worktree: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace worktree is not a regular directory")
	}
	common, err := gitPath(ctx, tree, "--git-common-dir")
	if err != nil {
		return fmt.Errorf("inspect worktree before cleanup: %w", err)
	}
	if common != owner.CommonDir {
		return errors.New("worktree belongs to a different repository")
	}
	if _, err = gitOutput(ctx, owner.Source, "worktree", "remove", "--force", "--", tree); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return os.RemoveAll(reservation)
}

func prepareWorkspace(ctx context.Context, statePath string, t Task) (string, string, error) {
	root, err := gitPath(ctx, t.Workspace, "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	source, err := filepath.EvalSymlinks(t.Workspace)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("workspace is outside source repository")
	}
	common, err := gitPath(ctx, root, "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	if !validTaskID(t.ID) {
		return "", "", errors.New("invalid task identity")
	}
	base, err := filepath.Abs(filepath.Join(filepath.Dir(statePath), "workspaces"))
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", "", err
	}
	reservation := filepath.Join(base, t.ID)
	tree := filepath.Join(reservation, "tree")
	owner := workspaceOwner{TaskID: t.ID, Source: root, CommonDir: common}
	created := false
	if err = os.Mkdir(reservation, 0700); err == nil {
		created = true
	} else if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(reservation)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("workspace reservation is not a regular directory")
		}
		saved, readErr := readWorkspaceOwner(reservation, t.ID)
		switch {
		case readErr == nil:
			if saved.Source != root || saved.CommonDir != common {
				return "", "", errors.New("workspace ownership does not match source")
			}
			owner = saved
		case emptyReservation(reservation):
			// A crash between Mkdir and the ownership write leaves a reservation
			// with no record and no worktree. Recreate it instead of failing
			// every later attempt.
			created = true
		default:
			return "", "", readErr
		}
	} else {
		return "", "", err
	}
	// A reservation this attempt created must not survive a failure, otherwise
	// it blocks every later attempt.
	defer func() {
		if !created {
			return
		}
		if _, statErr := os.Lstat(tree); statErr == nil {
			_ = discardBrokenWorktree(ctx, root, tree)
		}
		removeReservation(reservation)
	}()
	if created {
		if owner.Commit, err = gitOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			return "", "", err
		}
		if err = writeWorkspaceOwner(reservation, owner); err != nil {
			return "", "", err
		}
	}
	if info, statErr := os.Lstat(tree); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("execution workspace is not a regular directory")
		}
		if err = checkReservedWorktree(ctx, tree, common); err != nil {
			// An interrupted worktree add can leave a tree Git does not
			// recognize, and its stale registration blocks every retry. Remove
			// it, prune and add it once more.
			if removeErr := discardBrokenWorktree(ctx, root, tree); removeErr != nil {
				return "", "", fmt.Errorf("%w: %w; delete %s and retry", err, removeErr, tree)
			}
			if err = addReservedWorktree(ctx, root, tree, owner.Commit); err != nil {
				return "", "", fmt.Errorf("execution workspace %s is unusable: %w; delete the directory and retry", tree, err)
			}
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err = addReservedWorktree(ctx, root, tree, owner.Commit); err != nil {
			_ = discardBrokenWorktree(ctx, root, tree)
			return "", "", err
		}
	} else {
		return "", "", statErr
	}
	cwd := filepath.Join(tree, rel)
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("source subdirectory does not exist in committed snapshot")
	}
	created = false
	return cwd, owner.Commit, nil
}

// prepareTaskWorkspace serializes worktree creation with merge and discard,
// which mutate the same source repository.
func (s *Service) prepareTaskWorkspace(ctx context.Context, statePath string, t Task) (string, string, error) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	return prepareWorkspace(ctx, statePath, t)
}

func (s *Service) prepareExecution(ctx context.Context, t Task) (Task, error) {
	if !t.Isolated {
		return t, nil
	}
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	cwd, commit, err := s.prepareTaskWorkspace(ctx, path, t)
	if err != nil {
		return t, fmt.Errorf("prepare isolated workspace: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return t, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		current := &s.data.Tasks[i]
		if current.ID != t.ID {
			continue
		}
		old := clone(*current)
		current.ExecutionWorkspace = cwd
		current.IsolationCommit = commit
		current.Events = append(current.Events, Event{time.Now().UTC(), "workspace_prepared", "Execution workspace: " + cwd + " (initial commit " + commit + ")"})
		if err = s.save(); err != nil {
			*current = old
			return t, err
		}
		t.ExecutionWorkspace = cwd
		t.IsolationCommit = commit
		t.Workspace = cwd
		return t, nil
	}
	return t, errors.New("task not found during workspace preparation")
}
