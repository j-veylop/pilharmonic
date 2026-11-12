package orchestrator

import (
	"errors"
	"strings"
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
// queued subagent needs that slot. Its durable integration intent is reconciled
// after configuration at startup and after tracked workers become idle. A
// waiting task prepares a reviewable commit; an approved task merges exactly the
// commit the operator reviewed.
func (s *Service) resumeMergesLocked() {
	if s.closing {
		return
	}
	for i := range s.data.Tasks {
		owner := &s.data.Tasks[i]
		if owner.integrating || s.hasInheritedWorkspaceLocked(*owner, true) {
			continue
		}
		if owner.ReviewStatus == ReviewApproved && owner.ReviewCommit != "" &&
			owner.MergeStatus != MergeFailed && owner.MergeStatus != MergeMerged && owner.MergeStatus != MergeUpToDate {
			owner.integrating = true
			s.wg.Add(1)
			go s.finishReviewedMerge(clone(*owner))
			continue
		}
		if owner.MergeStatus == MergeWaiting && owner.ReviewStatus == "" {
			owner.integrating = true
			s.wg.Add(1)
			go s.finishDeferredPreparation(clone(*owner))
			continue
		}
		// A deferred preparation that stopped for an unsuccessful descendant is
		// re-entered once that descendant is gone, so deleting the child lets the
		// operator prepare the retained worktree without re-running the agent. A
		// Git failure keeps MergeFailed and is deliberately left for the operator.
		if mergeBlockedByDescendant(*owner) {
			if _, blocked := s.integrationBlockerLocked(*owner); blocked {
				continue
			}
			owner.integrating = true
			s.wg.Add(1)
			go s.finishDeferredPreparation(clone(*owner))
		}
	}
}

// mergeBlockedByDescendant reports a deferred preparation that failed only
// because an unsuccessful descendant still held the inherited worktree.
func mergeBlockedByDescendant(t Task) bool {
	return t.MergeStatus == MergeFailed && t.ReviewStatus == "" && strings.HasPrefix(t.MergeError, blockerMergeError)
}

func (s *Service) finishDeferredPreparation(owner Task) {
	defer s.wg.Done()
	s.mu.Lock()
	blocker, blocked := s.integrationBlockerLocked(owner)
	s.mu.Unlock()
	if blocked {
		s.finalizeIntegration(owner.ID, &blocker, nil)
		return
	}
	preparation, err := s.prepareIsolatedCommit(owner)
	if err != nil {
		outcome := mergeOutcome{Status: MergeFailed, Error: err.Error(), WorktreeCommit: preparation.WorktreeCommit}
		s.finalizeIntegration(owner.ID, &outcome, nil)
		return
	}
	s.finalizeIntegration(owner.ID, nil, &preparation)
}

func (s *Service) finishReviewedMerge(owner Task) {
	defer s.wg.Done()
	s.mu.Lock()
	blocker, blocked := s.integrationBlockerLocked(owner)
	s.mu.Unlock()
	if blocked {
		s.finalizeIntegration(owner.ID, &blocker, nil)
		return
	}
	outcome := s.mergeApprovedCommit(owner)
	s.finalizeIntegration(owner.ID, &outcome, nil)
}

// finalizeIntegration records one asynchronous integration result and resumes
// any other integration intent that became eligible.
func (s *Service) finalizeIntegration(id string, merge *mergeOutcome, preparation *preparationOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID != id {
			continue
		}
		t.integrating = false
		t.UpdatedAt = time.Now().UTC()
		if merge != nil {
			t.recordMerge(*merge)
		}
		if preparation != nil {
			t.recordPreparation(*preparation)
		}
		if err := s.save(); err != nil {
			s.persistErr = err
		}
		// The in-memory task is already advanced, so resuming is safe even when
		// the write failed: it only schedules work for other tasks and never
		// re-enters this one while integrating is cleared above.
		s.resumeMergesLocked()
		return
	}
}

func (task *Task) recordMerge(merge mergeOutcome) {
	task.MergeStatus = merge.Status
	task.MergeError = merge.Error
	task.MergeWarning = merge.Warning
	task.MergeCommit = merge.MergeCommit
	if merge.WorktreeCommit != "" {
		task.WorktreeCommit = merge.WorktreeCommit
	}
	task.MergedAt = time.Time{}
	eventKind, message := "merge_"+merge.Status, ""
	switch merge.Status {
	case MergeMerged:
		eventKind = "merged"
		task.MergedAt = task.UpdatedAt
		message = "Merged worktree commit " + task.WorktreeCommit + " into source (merge commit " + merge.MergeCommit + ")"
	case MergeUpToDate:
		eventKind = "merge_up_to_date"
		task.MergedAt = task.UpdatedAt
		message = "No unmerged isolated worktree changes"
	case MergeWaiting:
		message = "Integration waits for subagents using this worktree"
	case MergeFailed:
		eventKind = "merge_failed"
		message = "Isolated workspace merge failed: " + merge.Error
	}
	task.Events = append(task.Events, Event{task.UpdatedAt, eventKind, message})
	if merge.Status == MergeMerged && merge.Warning != "" {
		task.Events = append(task.Events, Event{task.UpdatedAt, "merge_warning", "Merge succeeded with a warning: " + merge.Warning})
	}
}

// recordPreparation records a commit prepared by an isolated attempt. A new
// commit opens review instead of merging; an up-to-date completion has nothing
// to review.
func (task *Task) recordPreparation(preparation preparationOutcome) {
	task.WorktreeCommit = preparation.WorktreeCommit
	task.MergeError = ""
	task.MergeWarning = ""
	task.MergeCommit = ""
	task.MergedAt = time.Time{}
	if preparation.UpToDate {
		task.MergeStatus = MergeUpToDate
		task.MergeCommit = preparation.SourceCommit
		task.MergedAt = task.UpdatedAt
		task.Events = append(task.Events, Event{task.UpdatedAt, "merge_up_to_date", "No unmerged isolated worktree changes"})
		return
	}
	task.MergeStatus = ""
	if !task.Isolated || preparation.WorktreeCommit == "" {
		return
	}
	if err := task.beginReview(preparation.WorktreeCommit); err != nil {
		// Leaving MergeStatus empty would hide a completed task that never
		// reached review, so surface it as a merge failure for the operator.
		task.MergeStatus = MergeFailed
		task.MergeError = "could not open review: " + err.Error()
		task.Events = append(task.Events, Event{task.UpdatedAt, "review_error", "Could not open review: " + err.Error()})
		return
	}
	task.Events = append(task.Events, Event{task.UpdatedAt, "review_pending", "Prepared worktree commit " + preparation.WorktreeCommit + " awaits review"})
}
