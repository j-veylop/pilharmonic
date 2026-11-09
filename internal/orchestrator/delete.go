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
