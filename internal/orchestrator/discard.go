package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const discardTimeout = 30 * time.Second

// DiscardWorktree removes the retained worktree of an isolated task. The task
// and its history are kept; only the worktree reservation and the task's
// recorded execution path are removed. Discarding is always an explicit
// operator action: it never runs automatically on merge or on task deletion.
//
// The worktree must be clean and its HEAD must already have been merged into
// the source checkout, so discarding never deletes uncommitted changes or
// unintegrated commits. Git operations never run while holding the service
// mutex.
func (s *Service) DiscardWorktree(id string) error {
	s.mu.Lock()
	t, err := s.discardableTaskLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.discarding[id] = true
	s.mu.Unlock()

	s.mergeMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), discardTimeout)
	err = discardWorkspace(ctx, s.path, t)
	cancel()
	s.mergeMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discarding, id)
	if err != nil {
		return err
	}
	for i := range s.data.Tasks {
		current := &s.data.Tasks[i]
		if current.ID != id {
			continue
		}
		old := clone(*current)
		path := current.ExecutionWorkspace
		current.ExecutionWorkspace = ""
		current.IsolationCommit = ""
		current.UpdatedAt = time.Now().UTC()
		current.Events = append(current.Events, Event{current.UpdatedAt, "worktree_discarded", "Discarded retained worktree " + path})
		if err = s.save(); err != nil {
			*current = old
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

// discardableTaskLocked validates that a task may have its retained worktree
// discarded. The recorded merge status is a cheap gate; discardWorkspace still
// verifies the worktree commit against the source checkout. Caller holds mu.
func (s *Service) discardableTaskLocked(id string) (Task, error) {
	if s.closing {
		return Task{}, errors.New("service is shutting down")
	}
	for _, t := range s.data.Tasks {
		if t.ID != id {
			continue
		}
		if !t.Isolated || t.ExecutionWorkspace == "" {
			return Task{}, errors.New("task has no retained isolated worktree")
		}
		if t.Status == Running {
			return Task{}, errors.New("task is running; cancel it and wait before discarding its worktree")
		}
		if _, active := s.cancel[t.ID]; active {
			return Task{}, errors.New("task is still stopping; wait before discarding its worktree")
		}
		if s.discarding[t.ID] {
			return Task{}, errors.New("worktree is already being discarded")
		}
		if t.MergeStatus != MergeMerged && t.MergeStatus != MergeUpToDate {
			return Task{}, errors.New("worktree has not been merged; resolve the merge before discarding")
		}
		return t, nil
	}
	return Task{}, errors.New("task not found")
}

// DeletableWorktrees reports how many retained worktrees are candidates for
// DiscardMergedWorktrees. A candidate is inactive and recorded as merged or up
// to date; each discard still re-checks the worktree, so a dirty or unmerged
// worktree can be refused later.
func (s *Service) DeletableWorktrees() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.discardCandidatesLocked())
}

// DiscardMergedWorktrees discards every retained worktree recorded as merged or
// up to date. Failures are collected so one refused worktree does not stop the
// others; it returns how many were removed.
func (s *Service) DiscardMergedWorktrees() (int, error) {
	s.mu.Lock()
	ids := s.discardCandidatesLocked()
	s.mu.Unlock()
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

// discardCandidatesLocked mirrors the checks in discardableTaskLocked without
// touching Git. Caller holds mu.
func (s *Service) discardCandidatesLocked() []string {
	var ids []string
	for _, t := range s.data.Tasks {
		if !t.Isolated || t.ExecutionWorkspace == "" {
			continue
		}
		if t.Status == Running || s.discarding[t.ID] {
			continue
		}
		if _, active := s.cancel[t.ID]; active {
			continue
		}
		if t.MergeStatus != MergeMerged && t.MergeStatus != MergeUpToDate {
			continue
		}
		ids = append(ids, t.ID)
	}
	return ids
}

// discardWorkspace removes a worktree after repeating the ownership checks made
// when it was prepared. Removal deliberately omits --force so Git refuses any
// worktree that still holds uncommitted or untracked files.
func discardWorkspace(ctx context.Context, statePath string, t Task) error {
	owner, tree, err := validateMergeWorkspace(ctx, statePath, t)
	if err != nil {
		return err
	}
	dirty, err := gitOutput(ctx, tree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if dirty != "" {
		return errors.New("worktree has uncommitted changes; commit or remove them before discarding")
	}
	head, err := gitOutput(ctx, tree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	sourceHead, err := gitOutput(ctx, owner.Source, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if _, err = gitOutput(ctx, owner.Source, "merge-base", "--is-ancestor", head, sourceHead); err != nil {
		return errors.New("worktree commit is not merged into the source checkout; merge it before discarding")
	}
	if _, err = gitOutput(ctx, owner.Source, "worktree", "remove", "--", tree); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	if err = os.RemoveAll(filepath.Dir(tree)); err != nil {
		return fmt.Errorf("remove worktree reservation: %w", err)
	}
	return nil
}
