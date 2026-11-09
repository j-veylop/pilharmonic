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

// SetTaskIsolation changes the workspace choice for a queued task. New change
// work defaults to an isolated worktree when its workspace supports one, while
// read-only inspection work is classified before acceptance. An operator choice
// is explicit and disables later automatic reclassification. Worktrees are
// preserved while the task exists, including across retries and review.
func (s *Service) SetTaskIsolation(id string, enabled bool) error {
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
		if t.Status != Queued || t.ScheduleRequested {
			return errors.New("isolation can only be changed on an unscheduled queued task")
		}
		if t.Isolated == enabled {
			if !t.IsolationAuto {
				return nil
			}
			old := clone(*t)
			t.IsolationAuto = false
			t.UpdatedAt = time.Now().UTC()
			t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", "Workspace choice confirmed by operator"})
			if err := s.save(); err != nil {
				*t = old
				return err
			}
			return nil
		}
		if t.ExecutionWorkspace != "" {
			return errors.New("prepared workspace must be retained for retries; create a new task to change isolation")
		}
		old := clone(*t)
		t.Isolated = enabled
		t.IsolationAuto = false
		t.UpdatedAt = time.Now().UTC()
		message := "Shared source workspace selected"
		if enabled {
			message = "Isolated committed-HEAD worktree selected; uncommitted files are excluded"
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", message})
		if err := s.save(); err != nil {
			*t = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
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

// removeTaskWorkspace validates the reservation's ownership record before
// removing anything. A missing reservation is a successful no-op, so shared
// workspace tasks and tasks that never prepared a worktree need no special
// handling.
func removeTaskWorkspace(ctx context.Context, statePath string, t Task) error {
	if statePath == "" || t.ID == "" || filepath.Base(t.ID) != t.ID || t.ID == "." || t.ID == ".." {
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
	if t.ID == "" || filepath.Base(t.ID) != t.ID || t.ID == "." || t.ID == ".." {
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
	if err = os.Mkdir(reservation, 0700); err == nil {
		owner.Commit, err = gitOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return "", "", err
		}
		b, _ := json.Marshal(owner)
		if err = os.WriteFile(filepath.Join(reservation, "owner.json"), b, 0600); err != nil {
			return "", "", err
		}
	} else if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(reservation)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("workspace reservation is not a regular directory")
		}
		saved, err := readWorkspaceOwner(reservation, t.ID)
		if err != nil {
			return "", "", err
		}
		if saved.Source != root || saved.CommonDir != common {
			return "", "", errors.New("workspace ownership does not match source")
		}
		owner = saved
	} else {
		return "", "", err
	}
	if info, statErr := os.Lstat(tree); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("execution workspace is not a regular directory")
		}
		actual, gitErr := gitPath(ctx, tree, "--git-common-dir")
		if gitErr != nil {
			return "", "", gitErr
		}
		if actual != common {
			return "", "", errors.New("execution workspace belongs to a different repository")
		}
		top, gitErr := gitPath(ctx, tree, "--show-toplevel")
		if gitErr != nil {
			return "", "", gitErr
		}
		if top != tree {
			return "", "", errors.New("execution workspace is not the reserved worktree")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if _, err = gitOutput(ctx, root, "worktree", "add", "--detach", "--", tree, owner.Commit); err != nil {
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
	return cwd, owner.Commit, nil
}

func (s *Service) prepareExecution(ctx context.Context, t Task) (Task, error) {
	if !t.Isolated {
		return t, nil
	}
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	cwd, commit, err := prepareWorkspace(ctx, path, t)
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
