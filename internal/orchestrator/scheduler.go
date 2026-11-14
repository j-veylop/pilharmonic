package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"pilharmonic/internal/worktree"
)

// DefaultConcurrency is the default number of simultaneous agent runs.
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
	return s.whileOpen(func() error {
		s.limit = limit
		return s.scheduleLocked()
	})
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
		t.reconcileAutoIsolation()
		t.Output = ""
		t.Error = ""
		t.clearIntegration()
		t.resetReview()
		// A task accepted before profiles existed records the one it runs with.
		if t.ProfileSpec == nil {
			t.ProfileName = profile.Name
			t.ProfileSpec = &profile
			t.Events = append(t.Events, Event{t.UpdatedAt, "profile", "Profile resolved: " + t.ProfileName})
		}
		s.grantControlLocked(t, profile)
		t.SpecialistTools = ceiling
		attempt := Attempt{ID: id, Agent: t.Agent, Tools: profile.Tools, Profile: profile.Name, Status: Running, StartedAt: t.UpdatedAt}
		if _, ok := runner.(FantasyRunner); ok {
			attempt.RequestedModel = s.fantasyModelLocked(t.Agent, &profile)
		} else if selector, ok := runner.(interface{ RequestedModel(*Profile) string }); ok {
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
	snapshot.model = t.Attempts[len(t.Attempts)-1].RequestedModel
	s.wg.Add(1)
	go s.run(runCtx, snapshot, runner)
	return nil
}

// reconcileAutoIsolation re-derives an automatic workspace choice from the
// immutable mandate before any workspace is prepared, including for state
// written by an older heuristic. Operator choices are preserved.
func (t *Task) reconcileAutoIsolation() {
	if !t.IsolationAuto || t.ExecutionWorkspace != "" {
		return
	}
	isolated := worktree.RequestsChanges(t.Prompt)
	if isolated == t.Isolated {
		return
	}
	t.Isolated = isolated
	message := "Shared source workspace selected; no requested change detected"
	if isolated {
		message = "Isolated worktree selected for requested change"
	}
	t.Events = append(t.Events, Event{t.UpdatedAt, "workspace", message})
}

// grantControlLocked issues this attempt's delegation credentials when the
// control socket is enabled and budget remains. Caller holds mu.
func (s *Service) grantControlLocked(t *Task, profile Profile) {
	t.controlSocket, t.controlToken, t.controlGuidance = "", "", ""
	remainingDepth := s.subagentMaxDepth - t.Depth
	if s.controlPath == "" || remainingDepth <= 0 || s.subagentMaxChildren <= 0 || profile.NoSubagents {
		return
	}
	remainingChildren := s.remainingSubagentChildrenLocked(t.ID)
	if remainingChildren <= 0 {
		return
	}
	t.controlSocket = s.controlPath
	t.controlToken = newID()
	t.controlGuidance = s.subagentGuidanceLocked(remainingDepth, remainingChildren)
	if t.ParentID == "" && t.Agent == DefaultRouteAgent && (len(profile.Tools) == 0 || slices.Contains(profile.Tools, "bash")) {
		t.controlGuidance += coordinatorGuidance(profile)
	}
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
		task := s.nextScheduledLocked()
		if task == nil {
			return nil
		}
		ctx := context.Background()
		if request := s.pending[task.ID]; request != nil {
			ctx = request.ctx
		}
		err := ctx.Err()
		if err == nil {
			err = s.startLocked(ctx, task)
		}
		switch {
		case err == nil:
			continue
		case ctx.Err() != nil:
			err = s.settleQueuedLocked(task, Canceled, "Scheduled request canceled", ctx.Err())
		case isProfileError(err):
			// An ineligible profile fails its own task instead of wedging the
			// queue behind a start that can never succeed.
			err = s.settleQueuedLocked(task, Failed, "Execution parameters rejected", err)
		}
		if err != nil {
			s.persistErr = err
			return err
		}
	}
	return nil
}

// nextScheduledLocked returns the oldest start request whose executor exists.
func (s *Service) nextScheduledLocked() *Task {
	var next *Task
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.Status != Queued || !t.ScheduleRequested {
			continue
		}
		if _, ok := s.runners[t.Agent]; ok && (next == nil || t.ScheduledAt.Before(next.ScheduledAt)) {
			next = t
		}
	}
	return next
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
		if err := s.settleQueuedLocked(t, Failed, "Execution parameters rejected", fmt.Errorf("agent %q unavailable", t.Agent)); err != nil {
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

// settleQueuedLocked durably ends a queued request that will not start. A
// cause becomes the task error and completes the event label.
func (s *Service) settleQueuedLocked(t *Task, status Status, label string, cause error) error {
	if err := s.mutateTaskLocked(t, func(t *Task) error {
		t.Status = status
		t.ScheduleRequested = false
		t.ScheduledAt = time.Time{}
		t.UpdatedAt = time.Now().UTC()
		message := label
		if cause != nil {
			t.Error = cause.Error()
			message += ": " + t.Error
		}
		t.Events = append(t.Events, Event{t.UpdatedAt, string(status), message})
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
	if t := s.taskLocked(id); t != nil && t.Status == Queued && t.ScheduleRequested {
		if err := s.settleQueuedLocked(t, Canceled, "Scheduled request canceled", request.ctx.Err()); err != nil {
			s.persistErr = err
		} else if schedErr := s.scheduleLocked(); schedErr != nil {
			s.persistErr = schedErr
		}
	}
}

// DefaultAttemptRetention is how many attempts per task keep their full output,
// so an often-retried task cannot grow the state file by maxOutput per attempt.
// Older attempts keep their record, status, error and timing. Zero keeps all.
const DefaultAttemptRetention = 8

// SetAttemptRetention changes how many attempts per task keep their full
// output. It applies as each new attempt starts. Zero disables truncation.
func (s *Service) SetAttemptRetention(keep int) error {
	if keep < 0 {
		return errors.New("attempt retention must not be negative")
	}
	return s.whileOpen(func() error { s.attemptRetention = keep; return nil })
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
