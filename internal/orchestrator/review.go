package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Review states describe an operator's disposition of an isolated task's
// prepared worktree commit. They are independent of execution status and merge
// outcome: automatic merging is integration, not review, so a merged change
// can still await or later receive a review decision. A rejected change stays
// in its retained worktree; rejecting it records the decision but does not
// undo or prevent the automatic merge.
const (
	ReviewPending  = "pending"
	ReviewApproved = "approved"
	ReviewRejected = "rejected"
)

// AwaitingReview reports whether prepared isolated work still needs an
// operator decision.
func (t Task) AwaitingReview() bool { return t.ReviewStatus == ReviewPending }

// ReviewSettled reports whether an operator has approved or rejected prepared
// isolated work.
func (t Task) ReviewSettled() bool {
	return t.ReviewStatus == ReviewApproved || t.ReviewStatus == ReviewRejected
}

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
// commit currently awaiting review.
func (s *Service) ApproveReview(id, note string) error {
	return s.recordReview(id, ReviewApproved, note)
}

// RejectReview records the local operator's rejection of the prepared worktree
// commit currently awaiting review. The commit and worktree are retained; the
// decision does not undo an automatic merge.
func (s *Service) RejectReview(id, note string) error {
	return s.recordReview(id, ReviewRejected, note)
}

// recordReview persists an operator decision with its event. The decision is
// matched against the stored commit so a stale caller cannot decide on
// different work. Caller state is rolled back if the save fails.
func (s *Service) recordReview(id, decision, note string) error {
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
		old := clone(*t)
		if err := t.settleReview(decision, t.ReviewCommit, LocalActor, note); err != nil {
			return err
		}
		t.UpdatedAt = t.ReviewedAt
		message := "Review " + decision + " by " + LocalActor
		if t.ReviewNote != "" {
			message += ": " + t.ReviewNote
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, "review_" + decision, message})
		if err := s.save(); err != nil {
			*t = old
			return err
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
