package orchestrator

import (
	"errors"
	"fmt"
)

// DeleteTask permanently removes an inactive task, its history and its
// dedicated worktree. If no other task references its thread, the thread and
// its routing history are removed as well. Files in the source workspace are
// not touched. The task is deleted durably before its worktree is cleaned up;
// a cleanup failure leaves the retained worktree and is reported.
func (s *Service) DeleteTask(id string) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return errors.New("service is shutting down")
	}
	for i, t := range s.data.Tasks {
		if t.ID != id {
			continue
		}
		if t.MergeStatus == MergeWaiting {
			s.mu.Unlock()
			return errors.New("task worktree is awaiting integration")
		}
		if t.integrating {
			s.mu.Unlock()
			return errors.New("task worktree is being integrated; wait before deleting")
		}
		if t.DiscardPending {
			s.mu.Unlock()
			return errors.New("worktree is being discarded; wait before deleting")
		}
		if t.Status == Running {
			s.mu.Unlock()
			return fmt.Errorf("task is running; cancel it and wait before deleting")
		}
		if _, active := s.cancel[id]; active {
			s.mu.Unlock()
			return errors.New("task is still stopping; wait before deleting")
		}
		for _, other := range s.data.Tasks {
			if other.ParentID == t.ID {
				s.mu.Unlock()
				return errors.New("task has spawned subagents; delete them before deleting the parent")
			}
		}

		deleted := clone(t)
		tasks := make([]Task, 0, len(s.data.Tasks)-1)
		tasks = append(tasks, s.data.Tasks[:i]...)
		tasks = append(tasks, s.data.Tasks[i+1:]...)
		threadUsed := false
		for _, other := range tasks {
			if other.ThreadID == t.ThreadID {
				threadUsed = true
				break
			}
		}
		threads := s.data.Threads
		if !threadUsed {
			threads = make([]Thread, 0, len(s.data.Threads))
			for _, thread := range s.data.Threads {
				if thread.ID != t.ThreadID {
					threads = append(threads, thread)
				}
			}
		}
		oldTasks, oldThreads := s.data.Tasks, s.data.Threads
		s.data.Tasks, s.data.Threads = tasks, threads
		if err := s.save(); err != nil {
			s.data.Tasks, s.data.Threads = oldTasks, oldThreads
			s.mu.Unlock()
			return err
		}
		s.wg.Add(1)
		defer s.wg.Done()
		s.resumeMergesLocked()
		s.mu.Unlock()
		if err := s.cleanupTaskWorkspace(deleted); err != nil {
			return fmt.Errorf("task deleted but workspace cleanup failed: %w", err)
		}
		return nil
	}
	s.mu.Unlock()
	return errors.New("task not found")
}

// CompletedDeletionCandidates returns the IDs eligible for bulk deletion. A completed task is skipped while it still has a child task
// that would remain, so parent links never dangle.
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

// DeleteCompletedTasks permanently removes the still-eligible confirmed tasks, their
// threads when no remaining task uses them, and their dedicated worktrees.
// Files in the source workspace are not touched. It returns the number of
// deleted tasks. Cleanup continues for every task and reports joined failures;
// the deletions are already durable when a cleanup error is returned.
func (s *Service) DeleteCompletedTasks(ids []string) (int, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return 0, errors.New("service is shutting down")
	}
	confirmed := make(map[string]bool, len(ids))
	for _, id := range ids {
		confirmed[id] = true
	}
	eligible := s.deletableCompletedLocked(confirmed)
	if len(eligible) == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	tasks := make([]Task, 0, len(s.data.Tasks)-len(eligible))
	deleted := make([]Task, 0, len(eligible))
	usedThreads := make(map[string]bool)
	for _, task := range s.data.Tasks {
		if eligible[task.ID] {
			deleted = append(deleted, clone(task))
			continue
		}
		tasks = append(tasks, task)
		usedThreads[task.ThreadID] = true
	}
	threads := make([]Thread, 0, len(s.data.Threads))
	for _, thread := range s.data.Threads {
		if usedThreads[thread.ID] {
			threads = append(threads, thread)
		}
	}
	oldTasks, oldThreads := s.data.Tasks, s.data.Threads
	s.data.Tasks, s.data.Threads = tasks, threads
	if err := s.save(); err != nil {
		s.data.Tasks, s.data.Threads = oldTasks, oldThreads
		s.mu.Unlock()
		return 0, err
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
		return len(deleted), fmt.Errorf("deleted %d completed task(s) but workspace cleanup failed: %w", len(deleted), cleanupErr)
	}
	return len(deleted), nil
}

// deletableCompletedLocked computes the set of completed tasks whose children,
// if any, are also completed and eligible. Caller holds mu.
func (s *Service) deletableCompletedLocked(confirmed map[string]bool) map[string]bool {
	children := make(map[string][]string)
	for _, task := range s.data.Tasks {
		if task.ParentID != "" {
			children[task.ParentID] = append(children[task.ParentID], task.ID)
		}
	}
	eligible := make(map[string]bool)
	for _, task := range s.data.Tasks {
		if task.Status != Completed || task.MergeStatus == MergeWaiting || (confirmed != nil && !confirmed[task.ID]) {
			continue
		}
		if _, stopping := s.cancel[task.ID]; !stopping && !task.DiscardPending && !task.integrating {
			eligible[task.ID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for id := range eligible {
			for _, child := range children[id] {
				if !eligible[child] {
					delete(eligible, id)
					changed = true
					break
				}
			}
		}
	}
	return eligible
}
