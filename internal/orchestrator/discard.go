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
	gone, reconcileErr := reconcileDiscard(s.path, task)
	if !gone && reconcileErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), discardTimeout)
		err = discardWorkspace(ctx, s.path, task)
		cancel()
		// A failed Git invocation may still have removed the directory.
		gone, reconcileErr = reconcileDiscard(s.path, task)
	}
	s.mergeMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discarding, id)
	if reconcileErr != nil {
		// Keep the durable intent. An explicit retry or restart can reconcile it.
		return errors.Join(err, reconcileErr)
	}
	for i := range s.data.Tasks {
		current := &s.data.Tasks[i]
		if current.ID != id {
			continue
		}
		old := clone(*current)
		message := "Worktree retained"
		if err != nil {
			message += ": " + err.Error()
		}
		current.finishDiscard(gone, message)
		if saveErr := s.save(); saveErr != nil {
			*current = old
			return errors.Join(err, saveErr)
		}
		return err
	}
	return errors.New("task not found")
}

func (t *Task) finishDiscard(gone bool, message string) {
	t.DiscardPending = false
	t.UpdatedAt = time.Now().UTC()
	kind := "worktree_discard_interrupted"
	if gone {
		kind = "worktree_discarded"
		message = "Discarded retained worktree " + t.ExecutionWorkspace
		t.ExecutionWorkspace = ""
		t.IsolationCommit = ""
	}
	t.Events = append(t.Events, Event{t.UpdatedAt, kind, message})
}

// reconcileDiscard never removes a surviving tree. If Git removed it, only
// an empty reservation or the task's validated ownership record may remain.
func reconcileDiscard(statePath string, t Task) (bool, error) {
	if t.ID == "." || !filepath.IsLocal(t.ID) || filepath.Base(t.ID) != t.ID {
		return false, errors.New("invalid task identity for discard")
	}
	base := filepath.Join(filepath.Dir(statePath), "workspaces")
	info, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("workspace base is not a directory")
	}
	reservation := filepath.Join(base, t.ID)
	info, err = os.Lstat(reservation)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("workspace reservation is not a regular directory")
	}
	if _, err = os.Lstat(filepath.Join(reservation, "tree")); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := os.ReadDir(reservation)
	if err != nil {
		return false, err
	}
	if len(entries) > 0 {
		if len(entries) != 1 || entries[0].Name() != "owner.json" {
			return false, errors.New("unexpected files in discarded worktree reservation")
		}
		if _, err = readWorkspaceOwner(reservation, t.ID); err != nil {
			return false, err
		}
		if err = os.Remove(filepath.Join(reservation, "owner.json")); err != nil {
			return false, err
		}
	}
	return true, os.Remove(reservation)
}

func (s *Service) discardableTaskLocked(id string) (*Task, error) {
	if s.closing {
		return nil, errors.New("service is shutting down")
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID == id {
			return t, s.checkDiscardLocked(*t)
		}
	}
	return nil, errors.New("task not found")
}

// checkDiscardLocked checks state shared by the single and bulk operations.
func (s *Service) checkDiscardLocked(t Task) error {
	if !t.Isolated || t.ExecutionWorkspace == "" {
		return errors.New("task has no retained isolated worktree")
	}
	if t.Status == Running {
		return errors.New("task is running; cancel it and wait before discarding its worktree")
	}
	if _, active := s.cancel[t.ID]; active {
		return errors.New("task is still stopping; wait before discarding its worktree")
	}
	if s.discarding[t.ID] {
		return errors.New("worktree is already being discarded")
	}
	if t.integrating {
		return errors.New("worktree integration is in progress; wait before discarding")
	}
	if t.ScheduleRequested {
		return errors.New("worktree is scheduled for use")
	}
	if s.hasInheritedWorkspaceLocked(t, false) {
		return errors.New("worktree is still referenced by a subagent; delete its tasks before discarding")
	}
	if t.MergeStatus != MergeMerged && t.MergeStatus != MergeUpToDate {
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
		if s.checkDiscardLocked(t) == nil {
			ids = append(ids, t.ID)
		}
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
	return nil
}
