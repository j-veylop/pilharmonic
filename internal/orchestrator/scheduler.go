package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultConcurrency = 2

type pendingRequest struct {
	ctx  context.Context
	stop chan struct{}
}

func (s *Service) removePendingLocked(id string) {
	if pending := s.pending[id]; pending != nil {
		close(pending.stop)
		delete(s.pending, id)
	}
}

// SetConcurrencyLimit changes capacity without interrupting running work.
func (s *Service) SetConcurrencyLimit(limit int) error {
	if limit < 1 {
		return fmt.Errorf("concurrency limit must be at least 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.limit = limit
	return s.scheduleLocked()
}

// ResumeScheduled activates persisted start requests after configuration is loaded.
// It is explicit so startup can configure concurrency and register runners first.
func (s *Service) ResumeScheduled() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	return s.scheduleLocked()
}

// startLocked persists the assignment before launching a worker. Caller holds mu.
func (s *Service) startLocked(ctx context.Context, t *Task) error {
	runner, ok := s.runners[t.Agent]
	if !ok {
		return fmt.Errorf("agent %q unavailable", t.Agent)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := newID()
	runCtx, cancel := context.WithCancel(ctx)
	old := clone(*t)
	t.Status = Running
	t.ScheduleRequested = false
	t.ScheduledAt = time.Time{}
	t.UpdatedAt = time.Now().UTC()
	t.Output = ""
	t.Error = ""
	t.MergeStatus, t.MergeError, t.MergeCommit, t.WorktreeCommit = "", "", "", ""
	t.MergedAt = time.Time{}
	t.resetReview()
	if s.controlPath != "" && s.subagentMaxDepth > t.Depth && s.subagentMaxChildren > 0 {
		t.controlSocket = s.controlPath
		t.controlToken = newID()
		t.controlGuidance = s.subagentGuidanceLocked(s.subagentMaxDepth-t.Depth, s.subagentMaxChildren)
	} else {
		t.controlSocket, t.controlToken, t.controlGuidance = "", "", ""
	}
	t.Attempts = append(t.Attempts, Attempt{ID: id, Agent: t.Agent, Status: Running, StartedAt: t.UpdatedAt})
	t.recordStartDeliveries(id, t.UpdatedAt)
	t.Events = append(t.Events, Event{t.UpdatedAt, "started", "Agent started (attempt " + id + ")"})
	if err := s.save(); err != nil {
		*t = old
		cancel()
		return err
	}
	s.removePendingLocked(t.ID)
	s.cancel[t.ID] = cancel
	if _, ok := runner.(LiveRunner); ok {
		s.live[t.ID] = make(chan Trigger, 64)
	}
	snapshot := clone(*t)
	s.wg.Add(1)
	go s.run(runCtx, snapshot, runner)
	return nil
}

// scheduleLocked drains explicit requests in submission order while capacity exists.
func (s *Service) scheduleLocked() error {
	if s.closing {
		return nil
	}
	defer s.resumeMergesLocked()
	for len(s.cancel) < s.limit {
		next := -1
		for i := range s.data.Tasks {
			t := &s.data.Tasks[i]
			if t.Status != Queued || !t.ScheduleRequested {
				continue
			}
			if _, ok := s.runners[t.Agent]; !ok {
				continue
			}
			if next < 0 || t.ScheduledAt.Before(s.data.Tasks[next].ScheduledAt) {
				next = i
			}
		}
		if next < 0 {
			return nil
		}
		task := &s.data.Tasks[next]
		ctx := context.Background()
		if request := s.pending[task.ID]; request != nil {
			ctx = request.ctx
		}
		if ctx.Err() != nil {
			if err := s.cancelPendingLocked(task, ctx.Err()); err != nil {
				s.persistErr = err
				return err
			}
			continue
		}
		if err := s.startLocked(ctx, task); err != nil {
			if ctx.Err() != nil {
				if err := s.cancelPendingLocked(task, ctx.Err()); err != nil {
					s.persistErr = err
					return err
				}
				continue
			}
			s.persistErr = err
			return err
		}
	}
	return nil
}

func (s *Service) cancelPendingLocked(t *Task, cause error) error {
	old := clone(*t)
	t.Status = Canceled
	t.ScheduleRequested = false
	t.ScheduledAt = time.Time{}
	t.UpdatedAt = time.Now().UTC()
	t.Error = cause.Error()
	t.Events = append(t.Events, Event{t.UpdatedAt, "canceled", "Scheduled request canceled: " + cause.Error()})
	if err := s.save(); err != nil {
		*t = old
		return err
	}
	s.removePendingLocked(t.ID)
	return nil
}
func (s *Service) watchPending(id string, request *pendingRequest) {
	select {
	case <-request.ctx.Done():
	case <-request.stop:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.pending[id] != request {
		return
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ID == id && t.Status == Queued && t.ScheduleRequested {
			if err := s.cancelPendingLocked(t, request.ctx.Err()); err != nil {
				s.persistErr = err
			} else {
				_ = s.scheduleLocked()
			}
			return
		}
	}
}
