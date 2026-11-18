package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pilharmonic/internal/worktree"
)

// DeleteTask permanently removes an inactive task, its history, its dedicated
// worktree and its thread when no other task uses it. Source files are not
// touched. The deletion is durable before cleanup runs; a cleanup failure
// leaves the worktree and is reported.
func (s *Service) DeleteTask(id string) error {
	var deleted []Task
	if err := s.withTask(id, func(t *Task) error {
		if reason := s.deleteBlockerLocked(*t, false); reason != "" {
			return errors.New(reason)
		}
		if s.subagentChildCountLocked(t.ID) > 0 {
			return errors.New("task has spawned subagents; delete them before deleting the parent")
		}
		var err error
		if deleted, err = s.removeTasksLocked(map[string]bool{id: true}); err == nil {
			s.wg.Add(1)
		}
		return err
	}); err != nil {
		return err
	}
	defer s.wg.Done()
	if err := s.cleanupTaskWorkspace(deleted[0]); err != nil {
		return fmt.Errorf("task deleted but workspace cleanup failed: %w", err)
	}
	return nil
}

// deleteBlockerLocked names the first rule, other than remaining children,
// that keeps a task from deletion, or returns "". Bulk deletion takes only
// completed tasks and also keeps prepared work that is unreviewed or unmerged:
// it lives only in the task's worktree, which deletion removes with --force.
func (s *Service) deleteBlockerLocked(t Task, bulk bool) string {
	if bulk && t.Status != Completed {
		return "task is not completed"
	}
	if reason := s.busyBlockerLocked(t); reason != "" {
		return reason
	}
	switch {
	case t.MergeStatus == MergeWaiting:
		return "worktree integration is waiting for subagents"
	case bulk && t.ReviewStatus == ReviewPending:
		return "prepared worktree commit awaits review"
	case bulk && unmergedWorktree(t):
		return "prepared worktree commit is not merged"
	case t.DiscardPending:
		return "worktree discard is pending"
	}
	return ""
}

// busyBlockerLocked names a running, stopping or integrating task, which
// neither deletion nor a worktree discard may touch, or returns "".
func (s *Service) busyBlockerLocked(t Task) string {
	_, stopping := s.cancel[t.ID]
	switch {
	case t.Status == Running:
		return "task is running; cancel it and wait"
	case stopping:
		return "task is still stopping; wait for it to finish"
	case t.integrating:
		return "worktree integration is in progress"
	}
	return ""
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
	var deleted []Task
	var skipped []SkippedDeletion
	if err := s.whileOpen(func() error {
		confirmed := make(map[string]bool, len(ids))
		for _, id := range ids {
			confirmed[id] = true
		}
		eligible := s.deletableCompletedLocked(confirmed)
		skipped = s.skippedDeletionsLocked(ids, eligible)
		if len(eligible) == 0 {
			return nil
		}
		var err error
		if deleted, err = s.removeTasksLocked(eligible); err == nil {
			s.wg.Add(1)
		}
		return err
	}); err != nil || len(deleted) == 0 {
		return 0, skipped, err
	}
	defer s.wg.Done()
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
			if reason = s.deleteBlockerLocked(*t, true); reason == "" {
				reason = "a completed child task must be deleted first"
			}
		}
		skipped = append(skipped, SkippedDeletion{ID: id, Reason: reason})
	}
	return skipped
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
		if (confirmed == nil || confirmed[task.ID]) && s.deleteBlockerLocked(task, true) == "" {
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

const discardTimeout = 30 * time.Second

// DiscardWorktree removes the retained worktree of an isolated task, keeping
// the task and its history. It is always an explicit operator action. The
// worktree must be clean and merged, so discarding never deletes uncommitted
// changes or unintegrated commits. Git never runs while holding the mutex.
func (s *Service) DiscardWorktree(id string) error {
	return s.discardWorktree(id, false)
}

// ForceDiscardWorktree explicitly removes dirty or unmerged retained work while
// preserving the task and its history. Local worktree changes are discarded.
func (s *Service) ForceDiscardWorktree(id string) error {
	return s.discardWorktree(id, true)
}

func (s *Service) discardWorktree(id string, force bool) error {
	s.mu.Lock()
	t, err := s.discardableTaskLocked(id, force)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !t.DiscardPending {
		t.DiscardPending = true
		if err = s.save(); err != nil {
			t.DiscardPending = false
			s.mu.Unlock()
			return err
		}
	}
	task := *t
	s.discarding[id] = true
	s.wg.Add(1)
	defer s.wg.Done()
	s.mu.Unlock()

	s.mergeMu.Lock()
	// An explicit retry can encounter a reservation already removed by Git.
	gone, reconcileErr := worktree.ReconcileDiscard(s.worktreeSpec(task))
	if !gone && reconcileErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), discardTimeout)
		err = worktree.Discard(ctx, s.worktreeSpec(task), force)
		cancel()
		// A failed Git invocation may still have removed the directory.
		gone, reconcileErr = worktree.ReconcileDiscard(s.worktreeSpec(task))
	}
	s.mergeMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discarding, id)
	if reconcileErr != nil {
		// Keep the durable intent. An explicit retry or restart can reconcile it.
		return errors.Join(err, reconcileErr)
	}
	current := s.taskLocked(id)
	if current == nil {
		return errTaskNotFound
	}
	// A Git refusal is not an interruption; history must not claim one.
	kind, message := "worktree_discard_interrupted", "Worktree retained"
	if err != nil {
		kind, message = "worktree_discard_refused", message+": "+err.Error()
	}
	if saveErr := s.mutateTaskLocked(current, func(t *Task) error { t.finishDiscard(gone, kind, message); return nil }); saveErr != nil {
		if gone {
			return saveErr
		}
		return errors.Join(err, saveErr)
	}
	if gone {
		// Git removed the tree, so the discard succeeded even if the invocation
		// reported an error; it is kept only in the event message.
		return nil
	}
	return err
}

// finishDiscard records the outcome and clears the durable intent.
func (t *Task) finishDiscard(gone bool, kind, message string) {
	t.DiscardPending = false
	t.UpdatedAt = time.Now().UTC()
	if gone {
		kind = "worktree_discarded"
		message = "Discarded retained worktree " + t.ExecutionWorkspace
		t.ExecutionWorkspace = ""
		t.IsolationCommit = ""
	}
	t.Events = append(t.Events, Event{t.UpdatedAt, kind, message})
}

func (s *Service) discardableTaskLocked(id string, force bool) (*Task, error) {
	if s.closing {
		return nil, errShuttingDown
	}
	if t := s.taskLocked(id); t != nil {
		return t, s.checkDiscardLocked(*t, force)
	}
	return nil, errTaskNotFound
}

// checkDiscardLocked checks state shared by the single and bulk operations.
func (s *Service) checkDiscardLocked(t Task, force bool) error {
	if !t.Isolated || t.ExecutionWorkspace == "" {
		return errors.New("task has no retained isolated worktree")
	}
	if reason := s.busyBlockerLocked(t); reason != "" {
		return errors.New(reason)
	}
	if s.discarding[t.ID] {
		return errors.New("worktree is already being discarded")
	}
	if t.ScheduleRequested {
		return errors.New("worktree is scheduled for use")
	}
	if s.hasInheritedWorkspaceLocked(t, false) {
		return errors.New("worktree is still referenced by a subagent; delete its tasks before discarding")
	}
	if !force && t.MergeStatus != MergeMerged && t.MergeStatus != MergeUpToDate {
		return errors.New("worktree has not been merged; resolve the merge before discarding")
	}
	return nil
}

// DiscardMergedWorktrees rechecks each confirmed task and discards its worktree
// when eligible. Failures on one worktree do not prevent the remaining checks.
func (s *Service) DiscardMergedWorktrees(ids []string) (int, error) {
	removed := 0
	var errs []error
	for _, id := range ids {
		if err := s.DiscardWorktree(id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// WorktreeDiscardCandidates returns IDs of retained worktrees eligible for
// consideration. DiscardWorktree performs the Git checks when confirmed.
func (s *Service) WorktreeDiscardCandidates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, t := range s.data.Tasks {
		if s.checkDiscardLocked(t, false) == nil {
			ids = append(ids, t.ID)
		}
	}
	return ids
}
