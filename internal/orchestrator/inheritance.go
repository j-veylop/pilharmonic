package orchestrator

import (
	"errors"
	"time"
)

// A subagent inherits an exact execution directory. Descendants which continue
// to share that directory also retain it after restart, including between
// attempts. An isolated child integrates back into its inherited directory.
func (s *Service) hasInheritedWorkspaceLocked(owner Task, activeOnly bool) bool {
	if owner.ExecutionWorkspace == "" {
		return false
	}
	for _, t := range s.data.Tasks {
		if t.ParentID == "" || t.Workspace != owner.ExecutionWorkspace {
			continue
		}
		if !activeOnly || t.Status == Queued || t.Status == Running || t.MergeStatus == MergeWaiting {
			return true
		}
	}
	return false
}

func (s *Service) checkRequeueIntegrationLocked(t Task) error {
	if t.integrating || t.MergeStatus == MergeWaiting {
		return errors.New("wait for worktree integration before requeueing this task")
	}
	if t.ParentID != "" {
		for _, owner := range s.data.Tasks {
			if owner.integrating && owner.ExecutionWorkspace == t.Workspace {
				return errors.New("inherited worktree is being integrated")
			}
		}
	}
	return nil
}

// The owner's successful attempt releases its execution slot even when a
// queued subagent needs that slot. Its durable merge intent is reconciled
// after configuration at startup and after tracked workers become idle.
func (s *Service) resumeMergesLocked() {
	if s.closing {
		return
	}
	for i := range s.data.Tasks {
		owner := &s.data.Tasks[i]
		if owner.MergeStatus != MergeWaiting || owner.integrating || s.hasInheritedWorkspaceLocked(*owner, true) {
			continue
		}
		owner.integrating = true
		s.wg.Add(1)
		go s.finishDeferredMerge(clone(*owner))
	}
}

func (s *Service) finishDeferredMerge(owner Task) {
	defer s.wg.Done()
	outcome := s.autoMergeIsolated(owner)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != owner.ID {
			continue
		}
		t.integrating = false
		t.UpdatedAt = time.Now().UTC()
		t.recordMerge(outcome)
		if err := s.save(); err != nil {
			s.persistErr = err
		} else {
			s.resumeMergesLocked()
		}
		return
	}
}

func (task *Task) recordMerge(merge mergeOutcome) {

	task.MergeStatus = merge.Status
	task.MergeError = merge.Error
	task.MergeCommit = merge.MergeCommit
	task.WorktreeCommit = merge.WorktreeCommit
	task.MergedAt = time.Time{}
	eventKind, message := "merge_"+merge.Status, ""
	switch merge.Status {
	case MergeMerged:
		eventKind = "merged"
		task.MergedAt = task.UpdatedAt
		message = "Merged worktree commit " + merge.WorktreeCommit + " into source (merge commit " + merge.MergeCommit + ")"
	case MergeUpToDate:
		eventKind = "merge_up_to_date"
		task.MergedAt = task.UpdatedAt
		message = "No unmerged isolated worktree changes"
	case MergeWaiting:
		message = "Integration waits for subagents using this worktree"
	case MergeFailed:
		eventKind = "merge_failed"
		message = "Automatic isolated workspace merge failed: " + merge.Error
	}
	task.Events = append(task.Events, Event{task.UpdatedAt, eventKind, message})
	// Prepared isolated work is recorded for review separately from the merge
	// outcome. An up-to-date completion has no new commit to review, and a
	// merge that failed before committing has nothing reviewable yet.
	if task.Isolated && merge.WorktreeCommit != "" && merge.Status != MergeUpToDate {
		if reviewErr := task.beginReview(merge.WorktreeCommit); reviewErr != nil {
			task.Events = append(task.Events, Event{task.UpdatedAt, "review_error", "Could not open review: " + reviewErr.Error()})
		} else {
			task.Events = append(task.Events, Event{task.UpdatedAt, "review_pending", "Prepared worktree commit " + merge.WorktreeCommit + " awaits review"})
		}
	}
}
