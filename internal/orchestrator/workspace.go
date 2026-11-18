package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pilharmonic/internal/worktree"
)

// SetTaskIsolation changes the workspace choice of an unscheduled queued task.
// An operator choice is explicit and disables later automatic changes.
// Worktrees are kept while the task exists, across retries and review.
func (s *Service) SetTaskIsolation(id string, enabled bool) error {
	if enabled {
		// Validation runs Git, so it must not hold the service mutex.
		var workspace string
		if err := s.withTask(id, func(t *Task) error { workspace = t.Workspace; return nil }); err != nil {
			return err
		}
		if !worktree.Isolatable(workspace) {
			if err := s.updateTask(id, func(t *Task) error {
				t.UpdatedAt = time.Now().UTC()
				t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", "Isolation unavailable: workspace is not a Git repository with a committed HEAD"})
				return nil
			}); err != nil {
				return err
			}
			return errors.New("workspace cannot be isolated: it is not a Git repository with a committed HEAD")
		}
	}
	return s.updateTask(id, func(t *Task) error {
		if t.Status != Queued || t.ScheduleRequested {
			return errors.New("isolation can only be changed on an unscheduled queued task")
		}
		message := "Workspace choice confirmed by operator"
		switch {
		case t.Isolated == enabled && !t.IsolationAuto:
			return nil
		case t.Isolated == enabled:
		case t.ExecutionWorkspace != "":
			return errors.New("prepared workspace must be retained for retries; create a new task to change isolation")
		case enabled:
			message = "Isolated committed-HEAD worktree selected; uncommitted files are excluded"
		default:
			message = "Shared source workspace selected"
		}
		t.Isolated, t.IsolationAuto = enabled, false
		t.UpdatedAt = time.Now().UTC()
		t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", message})
		return nil
	})
}

// prepareExecution prepares an isolated attempt's worktree, serialized with
// merge and discard, which mutate the same source repository, and records it
// before the runner starts.
func (s *Service) prepareExecution(ctx context.Context, t Task) (Task, error) {
	t.sourceWorkspace = t.Workspace
	if !t.Isolated {
		return t, nil
	}
	s.mergeMu.Lock()
	cwd, commit, err := worktree.Prepare(ctx, s.worktreeSpec(t))
	s.mergeMu.Unlock()
	if err != nil {
		return t, fmt.Errorf("prepare isolated workspace: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return t, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.taskLocked(t.ID)
	if current == nil {
		return t, errors.New("task not found during workspace preparation")
	}
	if err = s.mutateTaskLocked(current, func(current *Task) error {
		current.ExecutionWorkspace = cwd
		current.IsolationCommit = commit
		current.Events = append(current.Events, Event{time.Now().UTC(), "workspace_prepared", "Execution workspace: " + cwd + " (initial commit " + commit + ")"})
		return nil
	}); err != nil {
		return t, err
	}
	t.ExecutionWorkspace, t.IsolationCommit, t.Workspace = cwd, commit, cwd
	return t, nil
}

// ReviewDiff returns the patch from the isolation base to the exact commit awaiting review.
func (s *Service) ReviewDiff(ctx context.Context, id, commit string) (string, error) {
	t, ok := s.GetTask(id)
	if !ok {
		return "", errTaskNotFound
	}
	if !t.AwaitingReview() || t.ReviewCommit != commit || !t.Isolated || t.ExecutionWorkspace == "" {
		return "", errors.New("commit is not awaiting review for this isolated task")
	}
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	return worktree.ReviewDiff(ctx, s.worktreeSpec(t), commit)
}

// TaskDiffStat summarizes the worktree's line churn for the board. It uses the
// reviewed or prepared commit when one exists and the working tree otherwise.
func (s *Service) TaskDiffStat(ctx context.Context, id string) (added, removed int, err error) {
	t, ok := s.GetTask(id)
	if !ok {
		return 0, 0, errTaskNotFound
	}
	if !t.Isolated || t.ExecutionWorkspace == "" {
		return 0, 0, errors.New("task has no retained worktree")
	}
	commit := t.ReviewCommit
	if commit == "" {
		commit = t.WorktreeCommit
	}
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	return worktree.DiffStat(ctx, s.worktreeSpec(t), commit)
}

const workspaceCleanupTimeout = 30 * time.Second

// cleanupTaskWorkspace removes a deleted task's worktree and reservation
// outside the service mutex. It never touches the source workspace.
func (s *Service) cleanupTaskWorkspace(t Task) error {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	return worktree.Remove(ctx, s.worktreeSpec(t))
}

// worktreeSpec names the task's worktree for the Git operations in package
// worktree; reservations live beside the state file.
func (s *Service) worktreeSpec(t Task) worktree.Spec {
	return worktree.Spec{StatePath: s.path, TaskID: t.ID, Workspace: t.Workspace, Execution: t.ExecutionWorkspace, Title: t.Title}
}
