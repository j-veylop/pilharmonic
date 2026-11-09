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

// SetTaskIsolation opts into a dedicated committed-HEAD worktree. Worktrees are
// preserved for review, including when a task is deleted.
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
			return nil
		}
		if t.ExecutionWorkspace != "" {
			return errors.New("prepared workspace must be retained for retries; create a new task to change isolation")
		}
		old := clone(*t)
		t.Isolated = enabled
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

type workspaceOwner struct{ TaskID, Source, CommonDir, Commit string }

func prepareWorkspace(ctx context.Context, statePath string, t Task) (string, string, error) {
	root, err := gitOutput(ctx, t.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
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
	common, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	common, err = filepath.EvalSymlinks(common)
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
		info, statErr = os.Lstat(filepath.Join(reservation, "owner.json"))
		if statErr != nil || !info.Mode().IsRegular() {
			return "", "", errors.New("workspace reservation has no valid ownership record")
		}
		b, readErr := os.ReadFile(filepath.Join(reservation, "owner.json"))
		if readErr != nil {
			return "", "", readErr
		}
		var saved workspaceOwner
		if json.Unmarshal(b, &saved) != nil || saved.TaskID != t.ID || saved.Source != root || saved.CommonDir != common || saved.Commit == "" {
			return "", "", errors.New("workspace ownership does not match task and source")
		}
		owner = saved
	} else {
		return "", "", err
	}
	if info, statErr := os.Lstat(tree); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("execution workspace is not a regular directory")
		}
		actual, gitErr := gitOutput(ctx, tree, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if gitErr != nil {
			return "", "", gitErr
		}
		actual, err = filepath.EvalSymlinks(actual)
		if err != nil || actual != common {
			return "", "", errors.New("execution workspace belongs to a different repository")
		}
		top, gitErr := gitOutput(ctx, tree, "rev-parse", "--show-toplevel")
		if gitErr != nil {
			return "", "", gitErr
		}
		top, err = filepath.EvalSymlinks(top)
		if err != nil || top != tree {
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
