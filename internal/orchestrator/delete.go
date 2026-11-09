package orchestrator

import (
	"errors"
	"fmt"
)

// DeleteTask permanently removes an inactive task and its history. If no
// other task references its thread, the thread and its routing history are
// removed as well. Files in the workspace are not touched.
func (s *Service) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	for i, t := range s.data.Tasks {
		if t.ID != id {
			continue
		}
		if t.Status == Running {
			return fmt.Errorf("task is running; cancel it and wait before deleting")
		}
		if _, active := s.cancel[id]; active {
			return errors.New("task is still stopping; wait before deleting")
		}
		for _, other := range s.data.Tasks {
			if other.ParentID == t.ID {
				return errors.New("task has spawned subagents; delete them before deleting the parent")
			}
		}

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
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

// DeletableCompletedTasks reports how many completed tasks DeleteCompletedTasks
// would remove. A completed task is skipped while it still has a child task
// that would remain, so parent links never dangle.
func (s *Service) DeletableCompletedTasks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deletableCompletedLocked())
}

// DeleteCompletedTasks permanently removes all eligible completed tasks and
// the threads that no remaining task uses. Files in the workspace are not
// touched. It returns the number of deleted tasks.
func (s *Service) DeleteCompletedTasks() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return 0, errors.New("service is shutting down")
	}
	eligible := s.deletableCompletedLocked()
	if len(eligible) == 0 {
		return 0, nil
	}
	tasks := make([]Task, 0, len(s.data.Tasks)-len(eligible))
	usedThreads := make(map[string]bool)
	for _, task := range s.data.Tasks {
		if eligible[task.ID] {
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
		return 0, err
	}
	return len(eligible), nil
}

// deletableCompletedLocked computes the set of completed tasks whose children,
// if any, are also completed and eligible. Caller holds mu.
func (s *Service) deletableCompletedLocked() map[string]bool {
	children := make(map[string][]string)
	for _, task := range s.data.Tasks {
		if task.ParentID != "" {
			children[task.ParentID] = append(children[task.ParentID], task.ID)
		}
	}
	eligible := make(map[string]bool)
	for _, task := range s.data.Tasks {
		if task.Status != Completed {
			continue
		}
		if _, stopping := s.cancel[task.ID]; !stopping {
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
