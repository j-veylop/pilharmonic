package orchestrator

import (
	"context"
	"errors"
	"strings"
	"time"

	"pilharmonic/internal/worktree"
)

// Merge outcomes are recorded separately from the execution attempt. A merge
// failure does not rewrite a successful process exit into a failed attempt.
const (
	MergeMerged   = "merged"
	MergeUpToDate = "up_to_date"
	MergeFailed   = "failed"
	MergeWaiting  = "waiting_for_subagents"
)

// blockerMergeError prefixes a merge failure caused by an unsuccessful
// descendant subagent rather than by Git. It marks a failure an operator can
// clear by resolving the child task instead of re-running the agent.
const blockerMergeError = "Inspect inherited worktree and resolve unsuccessful subagent "

type mergeOutcome struct {
	Status         string
	WorktreeCommit string
	MergeCommit    string
	Error          string
	// Warning records a successful merge that still left the source checkout
	// needing attention, such as uncommitted source edits Git stashed but could
	// not reapply. It never turns a merge into a failure.
	Warning string
}

type preparationOutcome = worktree.Preparation

// integrationBlockerLocked reports worktree state that prevents preparing or
// merging an isolated task. Caller holds mu. An active descendant still uses the
// inherited worktree, so integration waits; an unsuccessful descendant may have
// left partial edits that need inspection first.
func (s *Service) integrationBlockerLocked(owner Task) (mergeOutcome, bool) {
	if s.hasInheritedWorkspaceLocked(owner, true) {
		return mergeOutcome{Status: MergeWaiting}, true
	}
	for _, child := range s.data.Tasks {
		if child.ParentID == "" || child.Workspace != owner.ExecutionWorkspace {
			continue
		}
		if child.Status == Failed || child.Status == Interrupted || child.Status == Canceled || child.MergeStatus == MergeFailed {
			return mergeOutcome{Status: MergeFailed, Error: blockerMergeError + child.ID + " before retrying integration"}, true
		}
	}
	return mergeOutcome{}, false
}

// prepareIsolatedCommit commits worktree changes and records the resulting
// commit as reviewable work. It never changes the source checkout. Git
// operations never run while holding the service mutex.
func (s *Service) prepareIsolatedCommit(t Task) (preparationOutcome, error) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), worktree.MergeTimeout)
	defer cancel()
	return worktree.Commit(ctx, s.worktreeSpec(t))
}

// mergeApprovedCommit integrates exactly the reviewed commit. It deliberately
// does not commit or stage worktree changes, so content that appeared after
// review cannot ride along with the operator's approval. Merges are serialized
// because several worktrees may point at the same source checkout.
func (s *Service) mergeApprovedCommit(t Task) mergeOutcome {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	reviewed := strings.TrimSpace(t.ReviewCommit)
	if reviewed == "" || reviewed != t.ReviewCommit {
		return mergeOutcome{Status: MergeFailed, Error: "reviewed commit does not match the task review"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktree.MergeTimeout)
	defer cancel()
	result, err := worktree.Merge(ctx, s.worktreeSpec(t), reviewed)
	if err != nil {
		return mergeOutcome{Status: MergeFailed, Error: err.Error(), WorktreeCommit: result.WorktreeCommit}
	}
	outcome := mergeOutcome{Status: MergeMerged, WorktreeCommit: result.WorktreeCommit, MergeCommit: result.MergeCommit, Warning: result.Warning}
	if result.UpToDate {
		outcome.Status = MergeUpToDate
	}
	return outcome
}

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
	t := s.taskLocked(id)
	if t == nil {
		return
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
	// the write failed: it only starts integration of other tasks.
	s.resumeMergesLocked()
}

func (t *Task) recordMerge(merge mergeOutcome) {
	t.MergeStatus = merge.Status
	t.MergeError = merge.Error
	t.MergeWarning = merge.Warning
	t.MergeCommit = merge.MergeCommit
	if merge.WorktreeCommit != "" {
		t.WorktreeCommit = merge.WorktreeCommit
	}
	t.MergedAt = time.Time{}
	eventKind, message := "merge_"+merge.Status, ""
	switch merge.Status {
	case MergeMerged:
		eventKind = "merged"
		t.MergedAt = t.UpdatedAt
		message = "Merged worktree commit " + t.WorktreeCommit + " into source (merge commit " + merge.MergeCommit + ")"
	case MergeUpToDate:
		eventKind = "merge_up_to_date"
		t.MergedAt = t.UpdatedAt
		message = "No unmerged isolated worktree changes"
	case MergeWaiting:
		message = "Integration waits for subagents using this worktree"
	case MergeFailed:
		eventKind = "merge_failed"
		message = "Isolated workspace merge failed: " + merge.Error
	}
	t.Events = append(t.Events, Event{t.UpdatedAt, eventKind, message})
	if merge.Status == MergeMerged && merge.Warning != "" {
		t.Events = append(t.Events, Event{t.UpdatedAt, "merge_warning", "Merge succeeded with a warning: " + merge.Warning})
	}
}

// recordPreparation records a commit prepared by an isolated attempt. A new
// commit opens review instead of merging; an up-to-date completion has nothing
// to review.
func (t *Task) recordPreparation(preparation preparationOutcome) {
	t.WorktreeCommit = preparation.WorktreeCommit
	t.MergeError = ""
	t.MergeWarning = ""
	t.MergeCommit = ""
	t.MergedAt = time.Time{}
	if preparation.UpToDate {
		t.MergeStatus = MergeUpToDate
		t.MergeCommit = preparation.SourceCommit
		t.MergedAt = t.UpdatedAt
		t.Events = append(t.Events, Event{t.UpdatedAt, "merge_up_to_date", "No unmerged isolated worktree changes"})
		return
	}
	t.MergeStatus = ""
	if !t.Isolated || preparation.WorktreeCommit == "" {
		return
	}
	if err := t.beginReview(preparation.WorktreeCommit); err != nil {
		// Leaving MergeStatus empty would hide a completed task that never
		// reached review, so surface it as a merge failure for the operator.
		t.MergeStatus = MergeFailed
		t.MergeError = "could not open review: " + err.Error()
		t.Events = append(t.Events, Event{t.UpdatedAt, "review_error", "Could not open review: " + err.Error()})
		return
	}
	t.Events = append(t.Events, Event{t.UpdatedAt, "review_pending", "Prepared worktree commit " + preparation.WorktreeCommit + " awaits review"})
}

// MergeConflict reports whether a failed approved merge was caused by a
// conflict. The merge is always aborted, so this is an operator-facing
// classification of the retained error, not a separate merge outcome.
func (t Task) MergeConflict() bool {
	if t.MergeStatus != MergeFailed {
		return false
	}
	return strings.Contains(strings.ToLower(t.MergeError), "conflict")
}
