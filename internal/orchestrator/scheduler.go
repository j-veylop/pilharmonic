package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// DefaultConcurrency is the number of agent processes the service runs at once
// unless the operator sets a different limit. It matches the CLI default so the
// library and the launcher agree.
const DefaultConcurrency = 4

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

// startLocked persists the assignment before launching a worker. Caller holds mu.
func (s *Service) startLocked(ctx context.Context, t *Task) error {
	runner, ok := s.runners[t.Agent]
	if !ok {
		return fmt.Errorf("agent %q unavailable", t.Agent)
	}
	ceiling, err := intersectTools(t.SpecialistTools, s.specialistTools[t.Agent])
	if err != nil {
		return profileError{err}
	}
	profile, err := s.profileForTaskLocked(*t)
	if err != nil {
		return profileError{err}
	}
	if err = validateProfileForRunner(runner, profile); err != nil {
		return profileError{err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := newID()
	runCtx, cancel := context.WithCancel(ctx)
	if err := s.mutateTaskLocked(t, func(t *Task) error {
		t.Status = Running
		t.ScheduleRequested = false
		t.ScheduledAt = time.Time{}
		t.UpdatedAt = time.Now().UTC()
		// Reconcile an automatic choice with the immutable mandate before any
		// workspace is prepared. This also applies to state written by the previous
		// role-based heuristic. Explicit operator choices are preserved.
		if t.IsolationAuto && t.ExecutionWorkspace == "" {
			isolated := taskMakesChanges(t.Prompt)
			if isolated != t.Isolated {
				t.Isolated = isolated
				message := "Shared source workspace selected; no requested change detected"
				if isolated {
					message = "Isolated worktree selected for requested change"
				}
				t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", message})
			}
		}
		t.Output = ""
		t.Error = ""
		t.clearIntegration()
		t.resetReview()
		// A task accepted before profiles existed has no snapshot yet; record the one
		// it actually runs with.
		if t.ProfileSpec == nil {
			t.ProfileName = profile.Name
			t.ProfileSpec = &profile
			t.Events = append(t.Events, Event{t.UpdatedAt, "profile", "Profile resolved: " + t.ProfileName})
		}
		remainingDepth := s.subagentMaxDepth - t.Depth
		remainingChildren := 0
		if remainingDepth > 0 && s.subagentMaxChildren > 0 && !profile.NoSubagents {
			remainingChildren = s.remainingSubagentChildrenLocked(t.ID)
		}
		if s.controlPath != "" && remainingDepth > 0 && remainingChildren > 0 {
			t.controlSocket = s.controlPath
			t.controlToken = newID()
			t.controlGuidance = s.subagentGuidanceLocked(remainingDepth, remainingChildren)
			if t.ParentID == "" && t.Agent == DefaultRouteAgent &&
				(len(profile.Tools) == 0 || slices.Contains(profile.Tools, "bash")) {
				t.controlGuidance += coordinatorGuidance(profile)
			}
		} else {
			t.controlSocket, t.controlToken, t.controlGuidance = "", "", ""
		}
		t.SpecialistTools = ceiling
		attempt := Attempt{ID: id, Agent: t.Agent, Tools: profile.Tools, Profile: profile.Name, Status: Running, StartedAt: t.UpdatedAt}
		if selector, ok := runner.(interface{ RequestedModel(*Profile) string }); ok {
			attempt.RequestedModel = selector.RequestedModel(&profile)
		}
		t.Attempts = append(t.Attempts, attempt)
		if dropped := retainAttemptOutputs(t, s.attemptRetention); dropped > 0 {
			t.Events = append(t.Events, retentionEvent(t, dropped, s.attemptRetention))
		}
		t.recordStartDeliveries(id, t.UpdatedAt)
		t.Events = append(t.Events, Event{t.UpdatedAt, "started", "Agent started (attempt " + id + ")"})
		return nil
	}); err != nil {
		cancel()
		return err
	}
	s.removePendingLocked(t.ID)
	s.cancel[t.ID] = cancel
	if _, ok := runner.(LiveRunner); ok {
		s.live[t.ID] = make(chan Trigger, 64)
	}
	snapshot := clone(*t)
	snapshot.ProfileSpec = cloneProfileRef(&profile)
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
	// Diagnose unrunnable requests before looking for free capacity, so a task
	// whose executor is missing is reported even while every slot is busy.
	if err := s.failUnavailableAgentsLocked(); err != nil {
		s.persistErr = err
		return err
	}
	for len(s.cancel) < s.limit {
		next := -1
		for i := range s.data.Tasks {
			t := &s.data.Tasks[i]
			if t.Status != Queued || !t.ScheduleRequested {
				continue
			}
			if _, ok := s.runners[t.Agent]; !ok {
				// Already diagnosed by failUnavailableAgentsLocked.
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
			if err := s.cancelQueuedLocked(task, ctx.Err()); err != nil {
				s.persistErr = err
				return err
			}
			continue
		}
		if err := s.startLocked(ctx, task); err != nil {
			if ctx.Err() != nil {
				if err := s.cancelQueuedLocked(task, ctx.Err()); err != nil {
					s.persistErr = err
					return err
				}
				continue
			}
			// An ineligible profile fails its own task instead of wedging the
			// queue behind a start that can never succeed.
			if isProfileError(err) {
				if failErr := s.failIneligibleLocked(task, err); failErr != nil {
					s.persistErr = failErr
					return failErr
				}
				continue
			}
			s.persistErr = err
			return err
		}
	}
	return nil
}

// failUnavailableAgentsLocked fails scheduled tasks whose executor is not
// registered in this run. Nothing else revisits such a request, so skipping it
// would strand it silently. Failing is per task: it stays visible and can be
// requeued once the executor is available. Caller holds mu.
func (s *Service) failUnavailableAgentsLocked() error {
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.Status != Queued || !t.ScheduleRequested {
			continue
		}
		if _, ok := s.runners[t.Agent]; ok {
			continue
		}
		if err := s.failIneligibleLocked(t, fmt.Errorf("agent %q unavailable", t.Agent)); err != nil {
			return err
		}
	}
	return nil
}

// profileError marks a task whose recorded profile the selected executor cannot
// honor. It is reported to the caller and, in the queue, failed per task.
type profileError struct{ err error }

func (e profileError) Error() string { return e.err.Error() }

func isProfileError(err error) bool {
	var target profileError
	return errors.As(err, &target)
}

// failIneligibleLocked records a durable failure for a task that cannot start.
func (s *Service) failIneligibleLocked(t *Task, cause error) error {
	if err := s.mutateTaskLocked(t, func(t *Task) error {
		t.Status = Failed
		t.ScheduleRequested = false
		t.ScheduledAt = time.Time{}
		t.UpdatedAt = time.Now().UTC()
		t.Error = cause.Error()
		t.Events = append(t.Events, Event{t.UpdatedAt, "failed", "Execution parameters rejected: " + cause.Error()})
		return nil
	}); err != nil {
		return err
	}
	s.removePendingLocked(t.ID)
	return nil
}

func (s *Service) cancelQueuedLocked(t *Task, cause error) error {
	if err := s.mutateTaskLocked(t, func(t *Task) error {
		t.Status = Canceled
		t.ScheduleRequested = false
		t.ScheduledAt = time.Time{}
		t.UpdatedAt = time.Now().UTC()
		message := "Canceled before start"
		if cause != nil {
			t.Error = cause.Error()
			message = "Scheduled request canceled: " + t.Error
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, "canceled", message})
		return nil
	}); err != nil {
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
			if err := s.cancelQueuedLocked(t, request.ctx.Err()); err != nil {
				s.persistErr = err
			} else if schedErr := s.scheduleLocked(); schedErr != nil {
				s.persistErr = schedErr
			}
			return
		}
	}
}

// DefaultAttemptRetention is how many attempts per task keep their full output.
// A task that is retried many times would otherwise grow the state file by up
// to maxOutput for every attempt. Earlier attempt records, statuses, errors and
// timings remain; only their copied transcript is dropped, and the task records
// one event when that happens. Zero keeps every attempt output and is the
// explicit opt-out.
const DefaultAttemptRetention = 8

// SetAttemptRetention changes how many attempts per task keep their full
// output. It applies as each new attempt starts. Zero disables truncation.
func (s *Service) SetAttemptRetention(keep int) error {
	if keep < 0 {
		return errors.New("attempt retention must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("service is shutting down")
	}
	s.attemptRetention = keep
	return nil
}

// retainAttemptOutputs drops the copied transcript of attempts older than the
// newest keep attempts. It returns how many outputs it dropped so the caller
// can record one event, and it never touches attempt identity or outcome.
func retainAttemptOutputs(task *Task, keep int) int {
	if keep <= 0 || len(task.Attempts) <= keep {
		return 0
	}
	limit := len(task.Attempts) - keep
	dropped := 0
	for i := 0; i < limit; i++ {
		attempt := &task.Attempts[i]
		if attempt.Output == "" || attempt.OutputTruncated {
			continue
		}
		attempt.Output = ""
		attempt.OutputTruncated = true
		dropped++
	}
	return dropped
}

func retentionEvent(task *Task, dropped, keep int) Event {
	return Event{
		Time:    task.UpdatedAt,
		Kind:    "retention",
		Message: fmt.Sprintf("Dropped output for %d earlier attempt(s); %d newest kept in full", dropped, keep),
	}
}
