package orchestrator

import (
	"errors"
	"fmt"
)

// DeleteTask permanently removes an inactive task, its history, its dedicated
// worktree and its thread when no other task uses it. Source files are not
// touched. The deletion is durable before cleanup runs; a cleanup failure
// leaves the worktree and is reported.
func (s *Service) DeleteTask(id string) error {
	s.mu.Lock()
	var err error
	switch t := s.taskLocked(id); {
	case s.closing:
		err = errShuttingDown
	case t == nil:
		err = errTaskNotFound
	default:
		err = s.checkDeleteLocked(*t)
	}
	var deleted []Task
	if err == nil {
		deleted, err = s.removeTasksLocked(map[string]bool{id: true})
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.wg.Add(1)
	defer s.wg.Done()
	s.mu.Unlock()
	if err := s.cleanupTaskWorkspace(deleted[0]); err != nil {
		return fmt.Errorf("task deleted but workspace cleanup failed: %w", err)
	}
	return nil
}

func (s *Service) checkDeleteLocked(t Task) error {
	_, stopping := s.cancel[t.ID]
	switch {
	case t.MergeStatus == MergeWaiting:
		return errors.New("task worktree is awaiting integration")
	case t.integrating:
		return errors.New("task worktree is being integrated; wait before deleting")
	case t.DiscardPending:
		return errors.New("worktree is being discarded; wait before deleting")
	case t.Status == Running:
		return errors.New("task is running; cancel it and wait before deleting")
	case stopping:
		return errors.New("task is still stopping; wait before deleting")
	case s.subagentChildCountLocked(t.ID) > 0:
		return errors.New("task has spawned subagents; delete them before deleting the parent")
	}
	return nil
}

// removeTasksLocked durably deletes the named tasks and every thread no
// remaining task uses, restoring both if the save fails. Caller holds mu.
func (s *Service) removeTasksLocked(ids map[string]bool) ([]Task, error) {
	tasks := make([]Task, 0, len(s.data.Tasks))
	var deleted []Task
	used := make(map[string]bool)
	for _, t := range s.data.Tasks {
		if ids[t.ID] {
			deleted = append(deleted, clone(t))
			continue
		}
		tasks = append(tasks, t)
		used[t.ThreadID] = true
	}
	dropped := make(map[string]bool)
	for _, t := range deleted {
		dropped[t.ThreadID] = !used[t.ThreadID]
	}
	threads := make([]Thread, 0, len(s.data.Threads))
	for _, thread := range s.data.Threads {
		if !dropped[thread.ID] {
			threads = append(threads, thread)
		}
	}
	snapshot := s.data
	s.data.Tasks, s.data.Threads = tasks, threads
	if err := s.commitLocked(snapshot); err != nil {
		return nil, err
	}
	s.resumeMergesLocked()
	return deleted, nil
}

// CompletedDeletionCandidates returns the IDs eligible for bulk deletion. A
// completed task with a child that would remain is skipped, so parent links
// never dangle.
func (s *Service) CompletedDeletionCandidates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := s.deletableCompletedLocked(nil)
	var ids []string
	for _, t := range s.data.Tasks {
		if eligible[t.ID] {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

// SkippedDeletion explains why a requested task was left in place.
type SkippedDeletion struct {
	ID     string
	Reason string
}

// DeleteCompletedTasksDetailed permanently removes the confirmed tasks that
// are still eligible, as DeleteTask does, and explains every requested task
// it left in place instead of silently dropping stale IDs.
func (s *Service) DeleteCompletedTasksDetailed(ids []string) (int, []SkippedDeletion, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return 0, nil, errShuttingDown
	}
	confirmed := make(map[string]bool, len(ids))
	for _, id := range ids {
		confirmed[id] = true
	}
	eligible := s.deletableCompletedLocked(confirmed)
	skipped := s.skippedDeletionsLocked(ids, eligible)
	if len(eligible) == 0 {
		s.mu.Unlock()
		return 0, skipped, nil
	}
	deleted, err := s.removeTasksLocked(eligible)
	if err != nil {
		s.mu.Unlock()
		return 0, skipped, err
	}
	s.wg.Add(1)
	defer s.wg.Done()
	s.mu.Unlock()
	var cleanupErr error
	for _, task := range deleted {
		if err := s.cleanupTaskWorkspace(task); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("task %s: %w", task.ID, err))
		}
	}
	if cleanupErr != nil {
		return len(deleted), skipped, fmt.Errorf("deleted %d completed task(s) but workspace cleanup failed: %w", len(deleted), cleanupErr)
	}
	return len(deleted), skipped, nil
}

// skippedDeletionsLocked explains every requested task that is not eligible.
func (s *Service) skippedDeletionsLocked(ids []string, eligible map[string]bool) []SkippedDeletion {
	seen := make(map[string]bool, len(ids))
	skipped := make([]SkippedDeletion, 0, len(ids))
	for _, id := range ids {
		if seen[id] || eligible[id] {
			continue
		}
		seen[id] = true
		reason := "task not found"
		if t := s.taskLocked(id); t != nil {
			if reason = s.bulkDeleteBlockerLocked(*t); reason == "" {
				reason = "a completed child task must be deleted first"
			}
		}
		skipped = append(skipped, SkippedDeletion{ID: id, Reason: reason})
	}
	return skipped
}

// bulkDeleteBlockerLocked names the first rule, other than remaining
// children, that keeps a task from bulk deletion, or returns "". Prepared work
// that is unreviewed or unmerged lives only in the task's worktree, which
// deletion would remove with --force.
func (s *Service) bulkDeleteBlockerLocked(t Task) string {
	_, stopping := s.cancel[t.ID]
	switch {
	case t.Status != Completed:
		return "task is not completed"
	case t.MergeStatus == MergeWaiting:
		return "worktree integration is waiting for subagents"
	case t.ReviewStatus == ReviewPending:
		return "prepared worktree commit awaits review"
	case unmergedWorktree(t):
		return "prepared worktree commit is not merged"
	case t.DiscardPending:
		return "worktree discard is pending"
	case t.integrating:
		return "worktree integration is in progress"
	case stopping:
		return "task is still stopping"
	}
	return ""
}

// unmergedWorktree reports prepared work that is not in the source checkout
// yet, so its worktree still holds the only copy of it.
func unmergedWorktree(t Task) bool {
	return t.WorktreeCommit != "" && t.MergeStatus != MergeMerged && t.MergeStatus != MergeUpToDate
}

// deletableCompletedLocked returns the completed tasks that pass every bulk
// rule and whose children, if any, are eligible too. Caller holds mu.
func (s *Service) deletableCompletedLocked(confirmed map[string]bool) map[string]bool {
	eligible := make(map[string]bool)
	for _, task := range s.data.Tasks {
		if (confirmed == nil || confirmed[task.ID]) && s.bulkDeleteBlockerLocked(task) == "" {
			eligible[task.ID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, task := range s.data.Tasks {
			if task.ParentID != "" && eligible[task.ParentID] && !eligible[task.ID] {
				delete(eligible, task.ParentID)
				changed = true
			}
		}
	}
	return eligible
}
