package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Review states describe an operator's disposition of an isolated task's
// prepared worktree commit. They are independent of execution status and merge
// outcome, but they gate integration: a prepared commit is not merged until the
// operator approves exactly that commit. A rejected change stays in its
// retained worktree so a follow-up can add context and run another attempt.
const (
	ReviewPending  = "pending"
	ReviewApproved = "approved"
	ReviewRejected = "rejected"
)

// AwaitingReview reports whether prepared isolated work still needs an
// operator decision.
func (t Task) AwaitingReview() bool { return t.ReviewStatus == ReviewPending }

// beginReview records a prepared worktree commit as awaiting review. A task can
// have only one outstanding review, and the commit must be non-empty so a later
// decision can be tied to exactly what was reviewed. Caller holds mu.
func (t *Task) beginReview(commit string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return errors.New("review commit is required")
	}
	if t.ReviewStatus != "" {
		return fmt.Errorf("task review is already %s", t.ReviewStatus)
	}
	t.ReviewStatus = ReviewPending
	t.ReviewCommit = commit
	t.ReviewedBy = ""
	t.ReviewedAt = time.Time{}
	t.ReviewNote = ""
	return nil
}

// settleReview records an operator decision for the prepared commit. The
// decision must match the commit that is awaiting review so a stale approval
// cannot authorize different work. Caller holds mu.
func (t *Task) settleReview(status, commit, actor, note string) error {
	if status != ReviewApproved && status != ReviewRejected {
		return fmt.Errorf("invalid review decision %q", status)
	}
	if t.ReviewStatus == "" {
		return errors.New("task has no prepared work awaiting review")
	}
	if t.ReviewStatus != ReviewPending {
		return fmt.Errorf("task review is already %s", t.ReviewStatus)
	}
	commit = strings.TrimSpace(commit)
	actor = strings.TrimSpace(actor)
	if commit == "" || commit != t.ReviewCommit {
		return errors.New("review decision does not match the prepared commit")
	}
	if actor == "" {
		return errors.New("review actor is required")
	}
	t.ReviewStatus = status
	t.ReviewedBy = actor
	t.ReviewedAt = time.Now().UTC()
	t.ReviewNote = strings.TrimSpace(note)
	return nil
}

// ApproveReview records the local operator's approval of the prepared worktree
// commit currently awaiting review. The decision is persisted before this call
// returns; approval also starts an asynchronous, serialized merge of exactly
// that commit, so the caller is not blocked by Git. Merge progress and failure
// are recorded on the task, and a failed merge remains retryable.
func (s *Service) ApproveReview(id, commit, note string) error {
	return s.recordReview(id, ReviewApproved, commit, note)
}

// RejectReview records the local operator's rejection of the prepared worktree
// commit currently awaiting review. The commit and worktree are retained, no
// merge is scheduled, and a follow-up can add context and requeue the task for
// another attempt.
func (s *Service) RejectReview(id, commit, note string) error {
	return s.recordReview(id, ReviewRejected, commit, note)
}

// recordReview checks that the displayed commit still awaits review and
// persists the decision with its event. Approval of isolated work records a
// durable merge intent and schedules the merge worker; a failed save restores
// the previous state.
func (s *Service) recordReview(id, decision, commit, note string) error {
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
		if t.DiscardPending {
			return errors.New("worktree discard is pending; wait before reviewing the commit")
		}
		mergeScheduled := false
		if err := s.mutateTaskLocked(t, func(t *Task) error {
			if err := t.settleReview(decision, commit, LocalActor, note); err != nil {
				return err
			}
			t.UpdatedAt = t.ReviewedAt
			message := "Review " + decision + " by " + LocalActor
			if t.ReviewNote != "" {
				message += ": " + t.ReviewNote
			}
			t.Events = append(t.Events, Event{t.UpdatedAt, "review_" + decision, message})
			if decision == ReviewApproved && t.Isolated {
				if s.hasInheritedWorkspaceLocked(*t, true) {
					t.recordMerge(mergeOutcome{Status: MergeWaiting})
				} else {
					t.integrating = true
					mergeScheduled = true
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if mergeScheduled {
			s.wg.Add(1)
			go s.finishReviewedMerge(clone(*t))
		}
		return nil
	}
	return errors.New("task not found")
}

// resetReview clears a review decision because a new attempt invalidates it.
// The prepared worktree and its commit are deliberately left untouched; only
// the operator-facing decision is discarded.
func (t *Task) resetReview() {
	t.ReviewStatus = ""
	t.ReviewCommit = ""
	t.ReviewedBy = ""
	t.ReviewedAt = time.Time{}
	t.ReviewNote = ""
}

// clearIntegration drops a previous attempt's integration outcome. A new attempt
// must not inherit a merge result or commit from an attempt whose work it
// replaces.
func (t *Task) clearIntegration() {
	t.MergeStatus, t.MergeError, t.MergeWarning, t.MergeCommit, t.WorktreeCommit = "", "", "", "", ""
	t.MergedAt = time.Time{}
}
